package appidentity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testKeyID      = "test-key-1"
	otherKeyID     = "test-key-2"
	testAudience   = "notifs-delivery"
	testWorkflow   = ".github/workflows/prod-deploy.yml"
	testRepository = "4IRL/urls4irl"
	testWaitBudget = 5 * time.Second
)

var testNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

var (
	keyOnce        sync.Once
	signingKey     *rsa.PrivateKey
	attackerKey    *rsa.PrivateKey
	keyGenerateErr error
)

// testKeys returns two throwaway RSA keys, generated once per test binary: the key GitHub
// "signs" with and a different one an attacker holds.
func testKeys(testInstance *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	testInstance.Helper()
	keyOnce.Do(func() {
		var err error
		if signingKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			keyGenerateErr = err
			return
		}
		attackerKey, keyGenerateErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	if keyGenerateErr != nil {
		testInstance.Fatalf("generate test keys: %v", keyGenerateErr)
	}
	return signingKey, attackerKey
}

func publicJWK(keyID string, key *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func jwksDocument(testInstance *testing.T, entries ...map[string]string) []byte {
	testInstance.Helper()
	document, err := json.Marshal(map[string]any{"keys": entries})
	if err != nil {
		testInstance.Fatalf("marshal jwks: %v", err)
	}
	return document
}

// jwksServer serves a swappable JWKS document and counts requests.
type jwksServer struct {
	server   *httptest.Server
	mutex    sync.Mutex
	document []byte
	status   int
	delay    time.Duration
	hang     chan struct{}
	hits     atomic.Int32
}

func newJWKSServer(testInstance *testing.T, document []byte) *jwksServer {
	testInstance.Helper()
	served := &jwksServer{document: document, status: http.StatusOK, hang: make(chan struct{})}
	served.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		served.hits.Add(1)
		served.mutex.Lock()
		document, status, delay := served.document, served.status, served.delay
		served.mutex.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if status == http.StatusOK && delay < 0 {
			select {
			case <-served.hang:
			case <-request.Context().Done():
			}
			return
		}
		writer.WriteHeader(status)
		_, _ = writer.Write(document)
	}))
	testInstance.Cleanup(func() {
		close(served.hang)
		served.server.Close()
	})
	return served
}

func (served *jwksServer) set(document []byte, status int, delay time.Duration) {
	served.mutex.Lock()
	defer served.mutex.Unlock()
	served.document, served.status, served.delay = document, status, delay
}

func (served *jwksServer) url() string { return served.server.URL + "/.well-known/jwks" }

// fakeClock is a settable clock.
type fakeClock struct {
	mutex   sync.Mutex
	current time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{current: testNow} }

func (clock *fakeClock) now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.current
}

func (clock *fakeClock) advance(by time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.current = clock.current.Add(by)
}

// syncBuffer is a bytes.Buffer safe for a slog handler writing from several goroutines.
type syncBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *syncBuffer) Write(data []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.Write(data)
}

func (locked *syncBuffer) String() string {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.String()
}

func textLogger(logs *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// memoryReplay is an in-memory ReplayRecorder.
type memoryReplay struct {
	mutex    sync.Mutex
	seen     map[string]time.Time
	attempts int
	err      error
}

func newMemoryReplay() *memoryReplay { return &memoryReplay{seen: map[string]time.Time{}} }

func (replay *memoryReplay) RecordJTI(_ context.Context, jti string, expiresAt time.Time) (bool, error) {
	replay.mutex.Lock()
	defer replay.mutex.Unlock()
	replay.attempts++
	if replay.err != nil {
		return false, replay.err
	}
	if _, found := replay.seen[jti]; found {
		return false, nil
	}
	replay.seen[jti] = expiresAt
	return true, nil
}

func (replay *memoryReplay) attemptCount() int {
	replay.mutex.Lock()
	defer replay.mutex.Unlock()
	return replay.attempts
}

// validClaims returns a claim set GitHub could have issued for the test workflow.
func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":                 GitHubIssuer,
		"aud":                 testAudience,
		"exp":                 testNow.Add(10 * time.Minute).Unix(),
		"nbf":                 testNow.Add(-time.Minute).Unix(),
		"iat":                 testNow.Add(-time.Minute).Unix(),
		"jti":                 "jti-1",
		"repository":          testRepository,
		"repository_id":       "12345",
		"repository_owner_id": "678",
		"workflow_ref":        testRepository + "/" + testWorkflow + "@refs/heads/main",
		"ref":                 "refs/heads/main",
		"environment":         "production",
		"run_id":              "999",
	}
}

// signed signs claims with method and key, setting the kid header when keyID is non-empty.
func signed(testInstance *testing.T, method jwt.SigningMethod, key any, keyID string, claims jwt.MapClaims) string {
	testInstance.Helper()
	token := jwt.NewWithClaims(method, claims)
	if keyID != "" {
		token.Header["kid"] = keyID
	}
	raw, err := token.SignedString(key)
	if err != nil {
		testInstance.Fatalf("sign token: %v", err)
	}
	return raw
}

func signedValid(testInstance *testing.T, mutate func(jwt.MapClaims)) string {
	testInstance.Helper()
	key, _ := testKeys(testInstance)
	claims := validClaims()
	if mutate != nil {
		mutate(claims)
	}
	return signed(testInstance, jwt.SigningMethodRS256, key, testKeyID, claims)
}

// verifierFixture bundles a verifier with the pieces tests poke at.
type verifierFixture struct {
	verifier *Verifier
	server   *jwksServer
	clock    *fakeClock
	logs     *syncBuffer
	replay   *memoryReplay
}

func newFixture(testInstance *testing.T, mutate func(*VerifierConfig)) *verifierFixture {
	testInstance.Helper()
	key, _ := testKeys(testInstance)
	fixture := &verifierFixture{
		server: newJWKSServer(testInstance, jwksDocument(testInstance, publicJWK(testKeyID, &key.PublicKey))),
		clock:  newFakeClock(),
		logs:   &syncBuffer{},
		replay: newMemoryReplay(),
	}
	config := VerifierConfig{
		Audience: testAudience,
		JWKSURL:  fixture.server.url(),
		Now:      fixture.clock.now,
		Logger:   textLogger(fixture.logs),
	}
	if mutate != nil {
		mutate(&config)
	}
	verifier, err := NewVerifier(config)
	if err != nil {
		testInstance.Fatalf("NewVerifier: %v", err)
	}
	fixture.verifier = verifier
	return fixture
}

func (fixture *verifierFixture) verify(token string) (Claims, error) {
	ctx, cancel := context.WithTimeout(context.Background(), testWaitBudget)
	defer cancel()
	return fixture.verifier.Verify(ctx, token, fixture.replay)
}

func TestVerifyAcceptsAValidTokenAndReturnsItsClaims(testInstance *testing.T) {
	fixture := newFixture(testInstance, nil)

	claims, err := fixture.verify(signedValid(testInstance, nil))
	if err != nil {
		testInstance.Fatalf("Verify: %v", err)
	}

	want := Claims{
		Repository: testRepository, RepositoryID: 12345, RepositoryOwnerID: 678,
		WorkflowPath: testWorkflow, Ref: "refs/heads/main", Environment: "production",
		RunID: "999", JTI: "jti-1", ExpiresAt: testNow.Add(10 * time.Minute),
	}
	// time.Time is compared with Equal, not ==, which would also compare the location.
	if !claims.ExpiresAt.Equal(want.ExpiresAt) {
		testInstance.Errorf("expires at = %v, want %v", claims.ExpiresAt, want.ExpiresAt)
	}
	claims.ExpiresAt = want.ExpiresAt
	if claims != want {
		testInstance.Errorf("claims = %+v, want %+v", claims, want)
	}
	if recorded, found := fixture.replay.seen["jti-1"]; !found || !recorded.Equal(want.ExpiresAt.Add(ClockLeeway)) {
		testInstance.Errorf("jti recorded until %v (found %v), want exp + leeway %v", recorded, found, want.ExpiresAt.Add(ClockLeeway))
	}
}

func TestVerifyClaimVariants(testInstance *testing.T) {
	testCases := []struct {
		label  string
		mutate func(jwt.MapClaims)
		check  func(*testing.T, Claims)
	}{
		{
			label:  "no environment claim reads as empty",
			mutate: func(claims jwt.MapClaims) { delete(claims, "environment") },
			check: func(subTest *testing.T, claims Claims) {
				if claims.Environment != "" {
					subTest.Errorf("environment = %q, want empty", claims.Environment)
				}
			},
		},
		{
			label: "numeric ids are accepted as well as strings",
			mutate: func(claims jwt.MapClaims) {
				claims["repository_id"] = 12345
				claims["repository_owner_id"] = 678
				claims["run_id"] = 999
			},
			check: func(subTest *testing.T, claims Claims) {
				if claims.RepositoryID != 12345 || claims.RepositoryOwnerID != 678 || claims.RunID != "999" {
					subTest.Errorf("claims = %+v", claims)
				}
			},
		},
		{
			label:  "an array audience containing ours is accepted",
			mutate: func(claims jwt.MapClaims) { claims["aud"] = []string{"someone-else", testAudience} },
			check:  func(*testing.T, Claims) {},
		},
		{
			label: "the workflow path is cut at the first @, so a ref containing @ is harmless",
			mutate: func(claims jwt.MapClaims) {
				claims["workflow_ref"] = testRepository + "/" + testWorkflow + "@refs/heads/feat@x"
			},
			check: func(subTest *testing.T, claims Claims) {
				if claims.WorkflowPath != testWorkflow {
					subTest.Errorf("workflow path = %q, want %q", claims.WorkflowPath, testWorkflow)
				}
			},
		},
		{
			label:  "a token that expired inside the 60s leeway is accepted",
			mutate: func(claims jwt.MapClaims) { claims["exp"] = testNow.Add(-30 * time.Second).Unix() },
			check:  func(*testing.T, Claims) {},
		},
		{
			label:  "a token not yet valid by less than the leeway is accepted",
			mutate: func(claims jwt.MapClaims) { claims["nbf"] = testNow.Add(30 * time.Second).Unix() },
			check:  func(*testing.T, Claims) {},
		},
		{
			label:  "an iat slightly in the future is accepted",
			mutate: func(claims jwt.MapClaims) { claims["iat"] = testNow.Add(30 * time.Second).Unix() },
			check:  func(*testing.T, Claims) {},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			fixture := newFixture(subTest, nil)
			claims, err := fixture.verify(signedValid(subTest, testCase.mutate))
			if err != nil {
				subTest.Fatalf("Verify: %v", err)
			}
			testCase.check(subTest, claims)
		})
	}
}

func TestVerifyRejectsEveryBadToken(testInstance *testing.T) {
	signer, attacker := testKeys(testInstance)

	// token builds the raw token to verify; every case must be an ErrInvalidToken that never
	// reached the replay ledger.
	testCases := []struct {
		label string
		token func(*testing.T) string
	}{
		{"a bad signature", func(subTest *testing.T) string {
			return signed(subTest, jwt.SigningMethodRS256, attacker, testKeyID, validClaims())
		}},
		{"a payload altered after signing (otherwise fully valid)", func(subTest *testing.T) string {
			parts := strings.Split(signedValid(subTest, nil), ".")
			payload, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
			if decodeErr != nil {
				subTest.Fatalf("decode payload: %v", decodeErr)
			}
			var claims map[string]any
			if err := json.Unmarshal(payload, &claims); err != nil {
				subTest.Fatalf("unmarshal payload: %v", err)
			}
			claims["repository_id"] = "99999" // every other claim stays valid; only the signature can reject it
			altered, _ := json.Marshal(claims)
			parts[1] = base64.RawURLEncoding.EncodeToString(altered)
			return strings.Join(parts, ".")
		}},
		{"the wrong issuer", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["iss"] = "https://evil.example" })
		}},
		{"no issuer", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "iss") })
		}},
		{"the wrong audience", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["aud"] = "another-service" })
		}},
		{"no audience", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "aud") })
		}},
		{"an expired token", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["exp"] = testNow.Add(-2 * time.Minute).Unix() })
		}},
		{"no expiry", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "exp") })
		}},
		{"a not-yet-valid token", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["nbf"] = testNow.Add(2 * time.Minute).Unix() })
		}},
		{"an iat in the future", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["iat"] = testNow.Add(2 * time.Minute).Unix() })
		}},
		{"HS256", func(subTest *testing.T) string {
			return signed(subTest, jwt.SigningMethodHS256, []byte("shared-secret"), testKeyID, validClaims())
		}},
		{"RS384", func(subTest *testing.T) string {
			return signed(subTest, jwt.SigningMethodRS384, signer, testKeyID, validClaims())
		}},
		{"alg none", func(subTest *testing.T) string {
			return signed(subTest, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, testKeyID, validClaims())
		}},
		{"a missing jti", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "jti") })
		}},
		{"an empty jti", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["jti"] = "" })
		}},
		{"a missing kid", func(subTest *testing.T) string {
			return signed(subTest, jwt.SigningMethodRS256, signer, "", validClaims())
		}},
		{"an unknown kid", func(subTest *testing.T) string {
			return signed(subTest, jwt.SigningMethodRS256, signer, "no-such-key", validClaims())
		}},
		{"a missing repository_id", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "repository_id") })
		}},
		{"a non-numeric repository_id", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["repository_id"] = "abc" })
		}},
		{"a zero repository_id", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["repository_id"] = "0" })
		}},
		{"a missing repository_owner_id", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "repository_owner_id") })
		}},
		{"a boolean run_id", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["run_id"] = true })
		}},
		{"a non-numeric run_id", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { claims["run_id"] = "12ab" })
		}},
		{"a missing run_id", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "run_id") })
		}},
		{"a missing ref", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "ref") })
		}},
		{"a missing repository", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "repository") })
		}},
		{"a missing workflow_ref", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) { delete(claims, "workflow_ref") })
		}},
		{"a workflow_ref with no @", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) {
				claims["workflow_ref"] = testRepository + "/" + testWorkflow
			})
		}},
		{"a workflow_ref from another repository", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) {
				claims["workflow_ref"] = "4IRL/other/" + testWorkflow + "@refs/heads/main"
			})
		}},
		{"a workflow_ref with no path", func(subTest *testing.T) string {
			return signedValid(subTest, func(claims jwt.MapClaims) {
				claims["workflow_ref"] = testRepository + "/@refs/heads/main"
			})
		}},
		{"garbage", func(*testing.T) string { return "not.a.jwt" }},
		{"an empty token", func(*testing.T) string { return "" }},
		{"an oversized token", func(*testing.T) string { return strings.Repeat("a", maxTokenBytes+1) }},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			fixture := newFixture(subTest, nil)

			_, err := fixture.verify(testCase.token(subTest))

			if !errors.Is(err, ErrInvalidToken) {
				subTest.Fatalf("err = %v, want ErrInvalidToken", err)
			}
			if errors.Is(err, ErrKeysUnavailable) {
				subTest.Errorf("a bad token must not read as an unavailable key set: %v", err)
			}
			if attempts := fixture.replay.attemptCount(); attempts != 0 {
				subTest.Errorf("replay ledger touched %d times by a rejected token, want 0", attempts)
			}
		})
	}
}

func TestVerifyRejectsAReplayedJTI(testInstance *testing.T) {
	fixture := newFixture(testInstance, nil)
	token := signedValid(testInstance, nil)

	if _, err := fixture.verify(token); err != nil {
		testInstance.Fatalf("first exchange: %v", err)
	}
	_, err := fixture.verify(token)

	if !errors.Is(err, ErrReplayed) {
		testInstance.Fatalf("second exchange err = %v, want ErrReplayed", err)
	}
	if !errors.Is(err, ErrInvalidToken) {
		testInstance.Errorf("a replay must also be an ErrInvalidToken (401): %v", err)
	}
}

func TestVerifyDoesNotBurnTheJTIOfAnInvalidToken(testInstance *testing.T) {
	_, attacker := testKeys(testInstance)
	fixture := newFixture(testInstance, nil)

	forged := signed(testInstance, jwt.SigningMethodRS256, attacker, testKeyID, validClaims())
	if _, err := fixture.verify(forged); !errors.Is(err, ErrInvalidToken) {
		testInstance.Fatalf("forged token err = %v, want ErrInvalidToken", err)
	}

	if _, err := fixture.verify(signedValid(testInstance, nil)); err != nil {
		testInstance.Errorf("the genuine token with the same jti must still be accepted: %v", err)
	}
}

func TestVerifyReplayStoreFailureIsNotAnInvalidToken(testInstance *testing.T) {
	fixture := newFixture(testInstance, nil)
	storeErr := errors.New("database unavailable")
	fixture.replay.err = storeErr

	_, err := fixture.verify(signedValid(testInstance, nil))

	if !errors.Is(err, storeErr) {
		testInstance.Fatalf("err = %v, want the store error", err)
	}
	if errors.Is(err, ErrInvalidToken) {
		testInstance.Errorf("a store failure must not read as an invalid token (it is a 503, not a 401): %v", err)
	}
}

func TestVerifyRequiresAReplayRecorder(testInstance *testing.T) {
	fixture := newFixture(testInstance, nil)

	_, err := fixture.verifier.Verify(context.Background(), signedValid(testInstance, nil), nil)

	if err == nil {
		testInstance.Fatal("Verify with a nil recorder succeeded; it must fail closed")
	}
	if fixture.server.hits.Load() != 0 {
		testInstance.Error("a nil recorder must be refused before any work is done")
	}
}

func TestUnknownKidRefetchesAtMostOncePerMinute(testInstance *testing.T) {
	signer, _ := testKeys(testInstance)
	fixture := newFixture(testInstance, nil)
	unknown := signed(testInstance, jwt.SigningMethodRS256, signer, "rotated-key", validClaims())

	// A known kid costs the one initial fetch, then nothing.
	for index := range 3 {
		if _, err := fixture.verify(signedValid(testInstance, func(claims jwt.MapClaims) {
			claims["jti"] = "jti-known-" + strconv.Itoa(index)
		})); err != nil {
			testInstance.Fatalf("known kid: %v", err)
		}
	}
	if hits := fixture.server.hits.Load(); hits != 1 {
		testInstance.Fatalf("fetches after known-kid verifies = %d, want 1 (the initial fetch)", hits)
	}

	// An unknown kid right after the initial fetch is inside the minute: no refetch.
	for range 3 {
		if _, err := fixture.verify(unknown); !errors.Is(err, ErrInvalidToken) {
			testInstance.Fatalf("unknown kid err = %v, want ErrInvalidToken", err)
		}
	}
	if hits := fixture.server.hits.Load(); hits != 1 {
		testInstance.Fatalf("fetches after rapid unknown kids = %d, want still 1", hits)
	}

	// Just under a minute later: still no refetch.
	fixture.clock.advance(DefaultRefetchInterval - time.Second)
	if _, err := fixture.verify(unknown); !errors.Is(err, ErrInvalidToken) {
		testInstance.Fatalf("unknown kid err = %v, want ErrInvalidToken", err)
	}
	if hits := fixture.server.hits.Load(); hits != 1 {
		testInstance.Fatalf("fetches at 59s = %d, want still 1", hits)
	}

	// A minute after the last fetch the unknown kid is allowed one refetch (and, still
	// unknown, is rejected); the next one is blocked again.
	fixture.clock.advance(time.Second)
	if _, err := fixture.verify(unknown); !errors.Is(err, ErrInvalidToken) {
		testInstance.Fatalf("unknown kid err = %v, want ErrInvalidToken", err)
	}
	if hits := fixture.server.hits.Load(); hits != 2 {
		testInstance.Fatalf("fetches at 60s = %d, want 2", hits)
	}
	if _, err := fixture.verify(unknown); !errors.Is(err, ErrInvalidToken) {
		testInstance.Fatalf("unknown kid err = %v, want ErrInvalidToken", err)
	}
	if hits := fixture.server.hits.Load(); hits != 2 {
		testInstance.Fatalf("fetches right after the refetch = %d, want still 2", hits)
	}
}

func TestUnknownKidRefetchPicksUpARotatedKey(testInstance *testing.T) {
	signer, attacker := testKeys(testInstance)
	fixture := newFixture(testInstance, nil)
	if _, err := fixture.verify(signedValid(testInstance, nil)); err != nil {
		testInstance.Fatalf("initial verify: %v", err)
	}

	// GitHub rotates: the issuer now also serves otherKeyID, signed by a second key.
	fixture.server.set(jwksDocument(testInstance,
		publicJWK(testKeyID, &signer.PublicKey), publicJWK(otherKeyID, &attacker.PublicKey)), http.StatusOK, 0)
	fixture.clock.advance(DefaultRefetchInterval)

	claims := validClaims()
	claims["jti"] = "jti-rotated"
	rotated := signed(testInstance, jwt.SigningMethodRS256, attacker, otherKeyID, claims)
	if _, err := fixture.verify(rotated); err != nil {
		testInstance.Fatalf("a token under the rotated-in key must verify after the refetch: %v", err)
	}
	if hits := fixture.server.hits.Load(); hits != 2 {
		testInstance.Errorf("fetches = %d, want 2", hits)
	}
}

func TestConcurrentUnknownKidsShareOneFetch(testInstance *testing.T) {
	signer, _ := testKeys(testInstance)
	fixture := newFixture(testInstance, nil)
	fixture.server.set(jwksDocument(testInstance, publicJWK(testKeyID, &signer.PublicKey)), http.StatusOK, 100*time.Millisecond)
	unknown := signed(testInstance, jwt.SigningMethodRS256, signer, "rotated-key", validClaims())

	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := fixture.verify(unknown); !errors.Is(err, ErrInvalidToken) {
				testInstance.Errorf("err = %v, want ErrInvalidToken", err)
			}
		}()
	}
	group.Wait()

	if hits := fixture.server.hits.Load(); hits != 1 {
		testInstance.Errorf("concurrent unknown kids caused %d fetches, want 1", hits)
	}
}

func TestHangingJWKSFetchFailsFastAndIsLogged(testInstance *testing.T) {
	fixture := newFixture(testInstance, func(config *VerifierConfig) { config.FetchTimeout = 200 * time.Millisecond })
	fixture.server.set(nil, http.StatusOK, -1) // hang until the request is canceled

	started := time.Now()
	_, err := fixture.verify(signedValid(testInstance, nil))
	elapsed := time.Since(started)

	if !errors.Is(err, ErrKeysUnavailable) {
		testInstance.Fatalf("err = %v, want ErrKeysUnavailable", err)
	}
	if errors.Is(err, ErrInvalidToken) {
		testInstance.Errorf("a hung JWKS fetch is not an invalid token: %v", err)
	}
	if elapsed > 3*time.Second {
		testInstance.Errorf("the hung fetch took %v, want it bounded by the fetch timeout", elapsed)
	}
	logged := fixture.logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "oidc jwks fetch failed") {
		testInstance.Errorf("the failed fetch was not logged at error:\n%s", logged)
	}

	// The failed attempt counts against the once-a-minute budget: no hammering a dead issuer.
	if _, again := fixture.verify(signedValid(testInstance, nil)); !errors.Is(again, ErrKeysUnavailable) {
		testInstance.Errorf("second attempt err = %v, want ErrKeysUnavailable", again)
	}
	if hits := fixture.server.hits.Load(); hits != 1 {
		testInstance.Errorf("fetches = %d, want 1: a failed fetch is not retried within the minute", hits)
	}
}

// TestACallerHangingUpDuringTheFetchDoesNotPoisonTheSharedBudget: the JWKS fetch is shared work,
// so a client that cancels mid-fetch must not leave every other caller facing a failed fetch
// and a spent once-a-minute budget.
func TestACallerHangingUpDuringTheFetchDoesNotPoisonTheSharedBudget(testInstance *testing.T) {
	signer, _ := testKeys(testInstance)
	fixture := newFixture(testInstance, nil)
	fixture.server.set(jwksDocument(testInstance, publicJWK(testKeyID, &signer.PublicKey)), http.StatusOK, 300*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, _ = fixture.verifier.Verify(ctx, signedValid(testInstance, func(claims jwt.MapClaims) { claims["jti"] = "hung-up" }), fixture.replay)

	// The next, patient caller must get a good verdict without a second fetch.
	if _, err := fixture.verify(signedValid(testInstance, func(claims jwt.MapClaims) { claims["jti"] = "patient" })); err != nil {
		testInstance.Fatalf("a caller after the hang-up: %v", err)
	}
	if hits := fixture.server.hits.Load(); hits != 1 {
		testInstance.Errorf("fetches = %d, want 1: the hang-up's fetch completed and was cached", hits)
	}
}

// TestACancelledCallerStopsWaitingBehindAHeldRefresh: a caller queued behind an in-flight JWKS
// fetch must give up when its own context ends, with ErrKeysUnavailable (fail closed), rather
// than sit on the holder's fetch for the whole fetch timeout.
func TestACancelledCallerStopsWaitingBehindAHeldRefresh(testInstance *testing.T) {
	fixture := newFixture(testInstance, func(config *VerifierConfig) { config.FetchTimeout = 2 * time.Second })
	fixture.server.set(nil, http.StatusOK, -1) // the holder's fetch hangs until its timeout

	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_, _ = fixture.verify(signedValid(testInstance, func(claims jwt.MapClaims) { claims["jti"] = "holder" }))
	}()
	deadline := time.Now().Add(testWaitBudget)
	for fixture.server.hits.Load() == 0 {
		if time.Now().After(deadline) {
			testInstance.Fatal("the holder's fetch never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := fixture.verifier.Verify(ctx, signedValid(testInstance, func(claims jwt.MapClaims) { claims["jti"] = "waiter" }), fixture.replay)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrKeysUnavailable) {
		testInstance.Errorf("err = %v, want ErrKeysUnavailable", err)
	}
	if errors.Is(err, ErrInvalidToken) {
		testInstance.Errorf("a cancelled wait is not an invalid token: %v", err)
	}
	if elapsed > time.Second {
		testInstance.Errorf("the cancelled caller waited %v, want it to stop near its 100ms deadline", elapsed)
	}
	if hits := fixture.server.hits.Load(); hits != 1 {
		testInstance.Errorf("fetches = %d, want 1: the cancelled waiter must not start its own", hits)
	}
	<-holderDone
}

func TestDefaultFetchTimeoutIsFiveSeconds(testInstance *testing.T) {
	if DefaultFetchTimeout != 5*time.Second {
		testInstance.Fatalf("DefaultFetchTimeout = %v, want 5s", DefaultFetchTimeout)
	}
	fixture := newFixture(testInstance, nil)
	if fixture.verifier.fetchTimeout != 5*time.Second {
		testInstance.Errorf("an unconfigured verifier's fetch timeout = %v, want 5s", fixture.verifier.fetchTimeout)
	}
	if fixture.verifier.refetchInterval != time.Minute {
		testInstance.Errorf("an unconfigured verifier's refetch interval = %v, want 1m", fixture.verifier.refetchInterval)
	}
}

func TestUnusableJWKSIsUnavailableNotInvalid(testInstance *testing.T) {
	smallKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		testInstance.Fatalf("generate small key: %v", err)
	}
	testCases := []struct {
		label    string
		document []byte
		status   int
	}{
		{label: "a server error", document: []byte("boom"), status: http.StatusInternalServerError},
		{label: "not json", document: []byte("<html>"), status: http.StatusOK},
		{label: "no keys", document: []byte(`{"keys":[]}`), status: http.StatusOK},
		{label: "only a too-small key", document: jwksDocument(testInstance, publicJWK(testKeyID, &smallKey.PublicKey)), status: http.StatusOK},
		{
			label:    "only a non-signing key",
			document: jwksDocument(testInstance, map[string]string{"kty": "RSA", "kid": testKeyID, "use": "enc"}),
			status:   http.StatusOK,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			fixture := newFixture(subTest, nil)
			fixture.server.set(testCase.document, testCase.status, 0)

			_, verifyErr := fixture.verify(signedValid(subTest, nil))

			if !errors.Is(verifyErr, ErrKeysUnavailable) {
				subTest.Fatalf("err = %v, want ErrKeysUnavailable", verifyErr)
			}
			if fixture.replay.attemptCount() != 0 {
				subTest.Error("a token judged against no keys must not touch the replay ledger")
			}
		})
	}
}

func TestStaticJWKSFileIsUsedWithoutContactingAnIssuer(testInstance *testing.T) {
	signer, _ := testKeys(testInstance)
	path := filepath.Join(testInstance.TempDir(), "jwks.json")
	if err := os.WriteFile(path, jwksDocument(testInstance, publicJWK(testKeyID, &signer.PublicKey)), 0o600); err != nil {
		testInstance.Fatalf("write jwks: %v", err)
	}
	fixture := newFixture(testInstance, func(config *VerifierConfig) { config.JWKSFile = path })

	if _, err := fixture.verify(signedValid(testInstance, nil)); err != nil {
		testInstance.Fatalf("a token under the file's key must verify: %v", err)
	}
	unknown := signed(testInstance, jwt.SigningMethodRS256, signer, "not-in-file", validClaims())
	fixture.clock.advance(time.Hour)
	if _, err := fixture.verify(unknown); !errors.Is(err, ErrInvalidToken) {
		testInstance.Errorf("unknown kid err = %v, want ErrInvalidToken", err)
	}
	if hits := fixture.server.hits.Load(); hits != 0 {
		testInstance.Errorf("static mode contacted the issuer %d times, want 0", hits)
	}
	if !strings.Contains(fixture.logs.String(), "static dev JWKS file") {
		testInstance.Errorf("static mode must be announced loudly in the log:\n%s", fixture.logs.String())
	}
}

func TestNewVerifierRejectsBadConfiguration(testInstance *testing.T) {
	emptyKeys := filepath.Join(testInstance.TempDir(), "empty.json")
	if err := os.WriteFile(emptyKeys, []byte(`{"keys":[]}`), 0o600); err != nil {
		testInstance.Fatalf("write jwks: %v", err)
	}
	testCases := []struct {
		label  string
		config VerifierConfig
	}{
		{label: "no audience", config: VerifierConfig{}},
		{label: "a missing jwks file", config: VerifierConfig{Audience: testAudience, JWKSFile: "/no/such/file.json"}},
		{label: "a jwks file with no usable key", config: VerifierConfig{Audience: testAudience, JWKSFile: emptyKeys}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			if verifier, err := NewVerifier(testCase.config); err == nil || verifier != nil {
				subTest.Fatalf("NewVerifier = %v, %v; want an error", verifier, err)
			}
		})
	}
}
