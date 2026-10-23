package appidentity

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// GitHubIssuer is the only token issuer delivery-api trusts.
	GitHubIssuer = "https://token.actions.githubusercontent.com"
	// jwksPath is appended to the issuer to find its signing keys.
	jwksPath = "/.well-known/jwks"

	// DefaultFetchTimeout bounds one JWKS fetch, so a hanging issuer fails the exchange
	// instead of holding it open.
	DefaultFetchTimeout = 5 * time.Second
	// DefaultRefetchInterval is the shortest gap between two JWKS fetches. An unknown kid
	// triggers a refetch, so this is what stops forged kids from hammering the issuer.
	DefaultRefetchInterval = time.Minute
	// ClockLeeway is the tolerance on exp, nbf and iat for clock skew between GitHub and us.
	ClockLeeway = 60 * time.Second

	signingAlgorithm = "RS256"
	// minRSABits rejects a JWKS key too small to trust.
	minRSABits = 2048
	// maxJWKSBytes bounds a JWKS response body.
	maxJWKSBytes = 1 << 20
	// maxTokenBytes bounds the raw token we will parse; real GitHub tokens are about 1-2 KB.
	maxTokenBytes = 16 << 10
)

var (
	// ErrInvalidToken wraps every token check failure (signature, algorithm, iss, aud,
	// exp/nbf/iat, missing claims, missing or replayed jti). The wrapped text names the
	// specific reason for the log; callers must not return it to the client.
	ErrInvalidToken = errors.New("invalid identity token")
	// ErrReplayed is returned for a jti that was already exchanged. It is an ErrInvalidToken.
	ErrReplayed = fmt.Errorf("%w: jti already used", ErrInvalidToken)
	// ErrKeysUnavailable means the issuer's signing keys could not be fetched, so the token
	// could not be judged either way. It is not an ErrInvalidToken.
	ErrKeysUnavailable = errors.New("identity token signing keys unavailable")
)

// Claims are the verified GitHub claims delivery-api acts on.
type Claims struct {
	// Repository is "owner/name", display only.
	Repository        string
	RepositoryID      int64
	RepositoryOwnerID int64
	// WorkflowPath is the workflow file path inside the repository, e.g.
	// ".github/workflows/prod-deploy.yml".
	WorkflowPath string
	Ref          string
	// Environment is empty when the workflow ran with no GitHub environment.
	Environment string
	RunID       string
	JTI         string
	ExpiresAt   time.Time
}

// ReplayExpiry is when the token's jti may be forgotten: the token's expiry plus the clock
// leeway, after which no verifier would accept the token anyway.
func (claims Claims) ReplayExpiry() time.Time {
	return claims.ExpiresAt.Add(ClockLeeway)
}

// ReplayRecorder remembers token ids so each token can be exchanged once.
type ReplayRecorder interface {
	// RecordJTI records jti until expiresAt. It reports false, with no error, when jti was
	// already recorded: the token is a replay.
	RecordJTI(ctx context.Context, jti string, expiresAt time.Time) (fresh bool, err error)
}

// VerifierConfig configures NewVerifier. Audience is required. Everything else defaults for
// production; the remaining fields exist so tests can point the verifier at a local JWKS.
type VerifierConfig struct {
	// Audience is the aud every token must carry.
	Audience string
	// JWKSFile, when set, loads a static key set from disk and never contacts the issuer.
	// Dev stacks only (Config enforces the dev-mode flag).
	JWKSFile string

	// Issuer is the required iss. Empty means GitHubIssuer. Tests only.
	Issuer string
	// JWKSURL is where the key set is fetched. Empty means Issuer + "/.well-known/jwks".
	JWKSURL string
	// HTTPClient performs the JWKS fetch. Empty builds a client with FetchTimeout that
	// refuses redirects.
	HTTPClient *http.Client
	// FetchTimeout bounds one JWKS fetch. Zero means DefaultFetchTimeout.
	FetchTimeout time.Duration
	// RefetchInterval is the minimum gap between JWKS fetches. Zero means DefaultRefetchInterval.
	RefetchInterval time.Duration
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Logger receives JWKS fetch and rejection logs. Nil means slog.Default().
	Logger *slog.Logger
}

// Verifier checks that a GitHub Actions OIDC token is genuine, fresh and meant for this
// service. It is safe for concurrent use.
type Verifier struct {
	issuer          string
	jwksURL         string
	httpClient      *http.Client
	fetchTimeout    time.Duration
	refetchInterval time.Duration
	now             func() time.Time
	logger          *slog.Logger
	parser          *jwt.Parser
	static          bool

	// refreshGate serialises JWKS fetches, so concurrent unknown-kid lookups cost one fetch. It is
	// a 1-slot semaphore rather than a mutex so a waiting caller can give up when its context ends.
	refreshGate chan struct{}
	// keysMutex guards keys and lastFetch.
	keysMutex    sync.RWMutex
	keys         map[string]*rsa.PublicKey
	lastFetch    time.Time
	lastFetchErr error
}

// NewVerifier builds a Verifier. It fails when the audience is empty or a configured JWKS
// file is unreadable or holds no usable key, so a misconfiguration stops startup.
func NewVerifier(config VerifierConfig) (*Verifier, error) {
	if config.Audience == "" {
		return nil, errors.New("oidc audience is required")
	}
	verifier := &Verifier{
		issuer:          config.Issuer,
		jwksURL:         config.JWKSURL,
		httpClient:      config.HTTPClient,
		fetchTimeout:    config.FetchTimeout,
		refetchInterval: config.RefetchInterval,
		now:             config.Now,
		logger:          config.Logger,
		keys:            map[string]*rsa.PublicKey{},
		refreshGate:     make(chan struct{}, 1),
	}
	if verifier.issuer == "" {
		verifier.issuer = GitHubIssuer
	}
	if verifier.jwksURL == "" {
		verifier.jwksURL = verifier.issuer + jwksPath
	}
	if verifier.fetchTimeout <= 0 {
		verifier.fetchTimeout = DefaultFetchTimeout
	}
	if verifier.refetchInterval <= 0 {
		verifier.refetchInterval = DefaultRefetchInterval
	}
	if verifier.now == nil {
		verifier.now = time.Now
	}
	if verifier.logger == nil {
		verifier.logger = slog.Default()
	}
	if verifier.httpClient == nil {
		verifier.httpClient = &http.Client{
			Timeout: verifier.fetchTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	verifier.parser = jwt.NewParser(
		jwt.WithValidMethods([]string{signingAlgorithm}),
		jwt.WithIssuer(verifier.issuer),
		jwt.WithAudience(config.Audience),
		jwt.WithLeeway(ClockLeeway),
		jwt.WithTimeFunc(verifier.now),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)

	if config.JWKSFile != "" {
		contents, readErr := os.ReadFile(config.JWKSFile)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", JWKSFileEnvVar, readErr)
		}
		keys, parseErr := parseJWKS(contents)
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", JWKSFileEnvVar, parseErr)
		}
		verifier.keys = keys
		verifier.static = true
		verifier.logger.Warn("oidc verifier uses a static dev JWKS file: GitHub's signing keys are NOT trusted",
			"keys", len(keys))
	}
	return verifier, nil
}

// tokenClaims is the claim set parsed from the token body.
type tokenClaims struct {
	jwt.RegisteredClaims
	Repository        string `json:"repository"`
	RepositoryID      scalar `json:"repository_id"`
	RepositoryOwnerID scalar `json:"repository_owner_id"`
	WorkflowRef       string `json:"workflow_ref"`
	Ref               string `json:"ref"`
	Environment       string `json:"environment"`
	RunID             scalar `json:"run_id"`
}

// scalar reads a JSON string or non-negative integer as text. GitHub sends the numeric ids
// as strings, but accepting both keeps a format change from silently failing.
type scalar string

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// UnmarshalJSON implements json.Unmarshaler.
func (value *scalar) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	switch {
	case string(trimmed) == "null":
		*value = ""
		return nil
	case len(trimmed) > 0 && trimmed[0] == '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		*value = scalar(text)
		return nil
	case digitsOnly.Match(trimmed):
		*value = scalar(trimmed)
		return nil
	default:
		return errors.New("claim must be a string or a non-negative integer")
	}
}

// Verify checks rawToken and returns its claims. It checks, in order: the signature
// (RS256 only, key chosen by kid), iss, aud, exp (required), nbf and iat with ClockLeeway,
// the presence of every claim delivery-api needs, a non-empty jti, and finally that the jti
// was not exchanged before (recorded through replay, which must not be nil). Any check
// failure is an ErrInvalidToken; a JWKS that cannot be fetched is ErrKeysUnavailable; a
// replay-store failure is returned as is. The jti is recorded only once every other check
// has passed, so a token that fails an earlier check does not burn its jti.
func (verifier *Verifier) Verify(ctx context.Context, rawToken string, replay ReplayRecorder) (Claims, error) {
	if replay == nil {
		return Claims{}, errors.New("verify identity token: replay recorder is required")
	}
	if rawToken == "" || len(rawToken) > maxTokenBytes {
		return Claims{}, fmt.Errorf("%w: token empty or too large", ErrInvalidToken)
	}

	parsed := &tokenClaims{}
	_, parseErr := verifier.parser.ParseWithClaims(rawToken, parsed, func(token *jwt.Token) (any, error) {
		return verifier.signingKey(ctx, token)
	})
	if parseErr != nil {
		if errors.Is(parseErr, ErrKeysUnavailable) {
			return Claims{}, ErrKeysUnavailable
		}
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, parseErr)
	}

	claims, claimsErr := parsed.toClaims()
	if claimsErr != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, claimsErr)
	}

	fresh, recordErr := replay.RecordJTI(ctx, claims.JTI, claims.ReplayExpiry())
	if recordErr != nil {
		return Claims{}, fmt.Errorf("record jti: %w", recordErr)
	}
	if !fresh {
		return Claims{}, ErrReplayed
	}
	return claims, nil
}

// toClaims validates that every claim delivery-api needs is present and well formed.
func (parsed *tokenClaims) toClaims() (Claims, error) {
	if parsed.ID == "" {
		return Claims{}, errors.New("missing jti")
	}
	if parsed.ExpiresAt == nil {
		return Claims{}, errors.New("missing exp")
	}
	repositoryID, repositoryIDErr := positiveID(string(parsed.RepositoryID))
	if repositoryIDErr != nil {
		return Claims{}, fmt.Errorf("repository_id: %w", repositoryIDErr)
	}
	ownerID, ownerIDErr := positiveID(string(parsed.RepositoryOwnerID))
	if ownerIDErr != nil {
		return Claims{}, fmt.Errorf("repository_owner_id: %w", ownerIDErr)
	}
	workflowPath, workflowErr := workflowPathFromRef(parsed.WorkflowRef, parsed.Repository)
	if workflowErr != nil {
		return Claims{}, fmt.Errorf("workflow_ref: %w", workflowErr)
	}
	if parsed.Repository == "" {
		return Claims{}, errors.New("missing repository")
	}
	if parsed.Ref == "" {
		return Claims{}, errors.New("missing ref")
	}
	if !digitsOnly.MatchString(string(parsed.RunID)) {
		return Claims{}, errors.New("run_id missing or not numeric")
	}
	return Claims{
		Repository:        parsed.Repository,
		RepositoryID:      repositoryID,
		RepositoryOwnerID: ownerID,
		WorkflowPath:      workflowPath,
		Ref:               parsed.Ref,
		Environment:       parsed.Environment,
		RunID:             string(parsed.RunID),
		JTI:               parsed.ID,
		ExpiresAt:         parsed.ExpiresAt.Time,
	}, nil
}

// positiveID parses a GitHub numeric id, which must be a positive integer.
func positiveID(text string) (int64, error) {
	value, parseErr := strconv.ParseInt(text, 10, 64)
	if parseErr != nil || value <= 0 {
		return 0, errors.New("missing or not a positive integer")
	}
	return value, nil
}

// workflowPathFromRef extracts the workflow file path from a workflow_ref claim, which is
// "<owner>/<repo>/<path>@<ref>": the part before the first "@", minus the repository prefix.
func workflowPathFromRef(workflowRef string, repository string) (string, error) {
	if repository == "" {
		return "", errors.New("repository claim is missing")
	}
	withoutRef, _, found := strings.Cut(workflowRef, "@")
	if !found {
		return "", errors.New("missing or malformed")
	}
	path, hasPrefix := strings.CutPrefix(withoutRef, repository+"/")
	if !hasPrefix || path == "" {
		return "", errors.New("does not belong to the repository claim")
	}
	return path, nil
}

// signingKey is the jwt key function: it returns the RSA public key for the token's kid.
func (verifier *Verifier) signingKey(ctx context.Context, token *jwt.Token) (any, error) {
	if token.Method.Alg() != signingAlgorithm {
		return nil, fmt.Errorf("unexpected signing algorithm %q", token.Method.Alg())
	}
	keyID, _ := token.Header["kid"].(string)
	if keyID == "" {
		return nil, errors.New("token has no kid")
	}
	key, keyErr := verifier.publicKey(ctx, keyID)
	if keyErr != nil {
		return nil, keyErr
	}
	return key, nil
}

// publicKey returns the cached key for keyID. An unknown kid triggers one JWKS refetch,
// rate-limited to one per refetch interval, then a second lookup.
func (verifier *Verifier) publicKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	if key := verifier.cachedKey(keyID); key != nil {
		return key, nil
	}
	if verifier.static {
		return nil, errors.New("unknown kid")
	}
	if refreshErr := verifier.refresh(ctx); refreshErr != nil {
		return nil, refreshErr
	}
	if key := verifier.cachedKey(keyID); key != nil {
		return key, nil
	}
	return nil, errors.New("unknown kid")
}

func (verifier *Verifier) cachedKey(keyID string) *rsa.PublicKey {
	verifier.keysMutex.RLock()
	defer verifier.keysMutex.RUnlock()
	return verifier.keys[keyID]
}

// refresh fetches the JWKS unless one was attempted within the refetch interval. A failed
// attempt counts, so an unreachable issuer is not retried on every request. Concurrent
// callers queue on refreshGate and, once through, find the gap not yet elapsed and reuse
// the keys the first caller fetched. A queued caller whose context ends stops waiting and
// gets ErrKeysUnavailable (fail closed: it never proceeds as if the keys were refreshed).
func (verifier *Verifier) refresh(ctx context.Context) error {
	select {
	case verifier.refreshGate <- struct{}{}:
		defer func() { <-verifier.refreshGate }()
	case <-ctx.Done():
		return fmt.Errorf("%w: waiting for jwks refresh: %v", ErrKeysUnavailable, ctx.Err())
	}

	started := verifier.now()
	verifier.keysMutex.RLock()
	lastFetch, lastFetchErr := verifier.lastFetch, verifier.lastFetchErr
	verifier.keysMutex.RUnlock()
	if !lastFetch.IsZero() && started.Sub(lastFetch) < verifier.refetchInterval {
		// Debug, not Warn: forged-kid requests are unauthenticated, so they must not control log volume.
		verifier.logger.Debug("oidc jwks refetch skipped: rate limited",
			"min_interval", verifier.refetchInterval.String(), "last_fetch_failed", lastFetchErr != nil)
		if lastFetchErr != nil {
			return fmt.Errorf("%w: last fetch failed: %v", ErrKeysUnavailable, lastFetchErr)
		}
		return nil
	}

	keys, fetchErr := verifier.fetchKeys(ctx)
	elapsed := verifier.now().Sub(started)
	verifier.keysMutex.Lock()
	verifier.lastFetch = started
	verifier.lastFetchErr = fetchErr
	if fetchErr == nil {
		verifier.keys = keys
	}
	verifier.keysMutex.Unlock()
	if fetchErr != nil {
		verifier.logger.Error("oidc jwks fetch failed",
			"url", verifier.jwksURL, "duration", elapsed.String(), "error", fetchErr)
		return fmt.Errorf("%w: %v", ErrKeysUnavailable, fetchErr)
	}
	verifier.logger.Info("oidc jwks refreshed",
		"url", verifier.jwksURL, "keys", len(keys), "duration", elapsed.String())
	return nil
}

// fetchKeys downloads and parses the issuer's JWKS under the fetch timeout.
func (verifier *Verifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	// Detached from the caller's cancellation: the result is shared (it is cached, and a failure
	// stamps the once-a-minute budget for everyone), so one client hanging up mid-fetch must not
	// fail the fetch for all. The fetch timeout still bounds it.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), verifier.fetchTimeout)
	defer cancel()

	request, requestErr := http.NewRequestWithContext(fetchCtx, http.MethodGet, verifier.jwksURL, nil)
	if requestErr != nil {
		return nil, fmt.Errorf("build jwks request: %w", requestErr)
	}
	request.Header.Set("Accept", "application/json")
	response, doErr := verifier.httpClient.Do(request)
	if doErr != nil {
		return nil, fmt.Errorf("fetch jwks (timeout %s): %w", verifier.fetchTimeout, doErr)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch jwks: status %d", response.StatusCode)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxJWKSBytes))
	if readErr != nil {
		return nil, fmt.Errorf("read jwks: %w", readErr)
	}
	return parseJWKS(body)
}

// jwk is one JSON Web Key of the set.
type jwk struct {
	KeyType   string `json:"kty"`
	KeyID     string `json:"kid"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

// parseJWKS parses a JWKS document into RSA public keys by kid. Entries that are not usable
// RS256 signature keys of at least minRSABits are skipped; a set with none left is an error.
func parseJWKS(document []byte) (map[string]*rsa.PublicKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(document, &set); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, entry := range set.Keys {
		if entry.KeyType != "RSA" || entry.KeyID == "" {
			continue
		}
		if entry.Use != "" && entry.Use != "sig" {
			continue
		}
		if entry.Algorithm != "" && entry.Algorithm != signingAlgorithm {
			continue
		}
		key, keyErr := entry.publicKey()
		if keyErr != nil {
			continue
		}
		keys[entry.KeyID] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks holds no usable RS256 signing key")
	}
	return keys, nil
}

// publicKey decodes the key's modulus and exponent.
func (entry jwk) publicKey() (*rsa.PublicKey, error) {
	modulusBytes, modulusErr := base64.RawURLEncoding.DecodeString(entry.Modulus)
	if modulusErr != nil {
		return nil, modulusErr
	}
	exponentBytes, exponentErr := base64.RawURLEncoding.DecodeString(entry.Exponent)
	if exponentErr != nil {
		return nil, exponentErr
	}
	modulus := new(big.Int).SetBytes(modulusBytes)
	exponent := new(big.Int).SetBytes(exponentBytes)
	if modulus.BitLen() < minRSABits {
		return nil, errors.New("rsa key too small")
	}
	if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 || exponent.Bit(0) == 0 {
		return nil, errors.New("rsa exponent out of range")
	}
	return &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}, nil
}
