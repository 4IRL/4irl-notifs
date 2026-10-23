//go:build integration

package integration

// The local and CI stacks cannot get real GitHub OIDC tokens, so delivery-api runs in dev mode
// and trusts the throwaway key set scripts/dev-oidc-keys.mjs writes to .local/dev-oidc/ (made by
// make local-up and by CI's "Generate dev OIDC keys" step). These tests prove the stack really
// mounted that key set, and give later steps' tests devOIDCSigner to sign tokens with its private
// half. A failure here usually means .local/dev-oidc/ was generated after the container started
// (it mounted a stale or empty JWKS): rerun make local-up.

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
)

const (
	containerJWKSPath = "/run/dev-oidc/jwks.json"
	devOIDCAudience   = "notifs-delivery"
	devOIDCTokenTTL   = 10 * time.Minute
)

// devOIDCDir returns the repo's .local/dev-oidc directory, located from this source file so the
// working directory does not matter.
func devOIDCDir(testInstance *testing.T) string {
	testInstance.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		testInstance.Fatal("cannot locate the repo root via runtime.Caller")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", ".local", "dev-oidc")
}

// devOIDCSigner signs test GitHub OIDC tokens with the dev private key.
type devOIDCSigner struct {
	privateKey *rsa.PrivateKey
	keyID      string
}

// loadDevOIDCSigner reads the dev private key and the kid its JWKS entry carries. It fails the
// test, naming the fix, when the key pair has not been generated.
func loadDevOIDCSigner(testInstance *testing.T) devOIDCSigner {
	testInstance.Helper()
	dir := devOIDCDir(testInstance)

	pemBytes, readErr := os.ReadFile(filepath.Join(dir, "private.pem"))
	if readErr != nil {
		testInstance.Fatalf("read the dev OIDC private key (run make dev-oidc-keys): %v", readErr)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		testInstance.Fatal("private.pem holds no PEM block")
	}
	parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
	if parseErr != nil {
		testInstance.Fatalf("parse the dev OIDC private key: %v", parseErr)
	}
	privateKey, isRSA := parsed.(*rsa.PrivateKey)
	if !isRSA {
		testInstance.Fatalf("the dev OIDC private key is %T, want *rsa.PrivateKey", parsed)
	}

	var jwks struct {
		Keys []struct {
			KeyID string `json:"kid"`
		} `json:"keys"`
	}
	jwksBytes, jwksReadErr := os.ReadFile(filepath.Join(dir, "jwks.json"))
	if jwksReadErr != nil {
		testInstance.Fatalf("read the dev OIDC jwks (run make dev-oidc-keys): %v", jwksReadErr)
	}
	if decodeErr := json.Unmarshal(jwksBytes, &jwks); decodeErr != nil || len(jwks.Keys) != 1 {
		testInstance.Fatalf("dev OIDC jwks is not a one-key set: %v", decodeErr)
	}
	return devOIDCSigner{privateKey: privateKey, keyID: jwks.Keys[0].KeyID}
}

// sign returns an RS256 token carrying claims, with the dev kid header.
func (signer devOIDCSigner) sign(testInstance *testing.T, claims jwt.MapClaims) string {
	testInstance.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = signer.keyID
	raw, signErr := token.SignedString(signer.privateKey)
	if signErr != nil {
		testInstance.Fatalf("sign dev OIDC token: %v", signErr)
	}
	return raw
}

// workflowClaims returns the claims a GitHub deploy of the given workflow would carry, valid
// for devOIDCTokenTTL, with a jti unique to the call.
func workflowClaims(repository string, repositoryID string, workflowPath string, jti string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":                 appidentity.GitHubIssuer,
		"aud":                 devOIDCAudience,
		"iat":                 now.Add(-time.Minute).Unix(),
		"nbf":                 now.Add(-time.Minute).Unix(),
		"exp":                 now.Add(devOIDCTokenTTL).Unix(),
		"jti":                 jti,
		"repository":          repository,
		"repository_id":       repositoryID,
		"repository_owner_id": "4242",
		"workflow_ref":        repository + "/" + workflowPath + "@refs/heads/main",
		"ref":                 "refs/heads/main",
		"run_id":              "1",
	}
}

// onceReplay is an in-memory ReplayRecorder for tests that only verify a token.
type onceReplay struct{ seen map[string]bool }

func (replay *onceReplay) RecordJTI(_ context.Context, jti string, _ time.Time) (bool, error) {
	if replay.seen[jti] {
		return false, nil
	}
	replay.seen[jti] = true
	return true, nil
}

// TestDevOIDCKeyPairInteroperates signs a token with the dev private key and verifies it
// against the dev JWKS exactly as the dev-mode delivery-api does.
func TestDevOIDCKeyPairInteroperates(testInstance *testing.T) {
	signer := loadDevOIDCSigner(testInstance)
	verifier, newErr := appidentity.NewVerifier(appidentity.VerifierConfig{
		Audience: devOIDCAudience,
		JWKSFile: filepath.Join(devOIDCDir(testInstance), "jwks.json"),
	})
	if newErr != nil {
		testInstance.Fatalf("NewVerifier over the dev JWKS: %v", newErr)
	}
	replay := &onceReplay{seen: map[string]bool{}}
	token := signer.sign(testInstance, workflowClaims("4IRL/dev-app", "1001", ".github/workflows/deploy.yml", "dev-interop-1"))

	claims, verifyErr := verifier.Verify(context.Background(), token, replay)
	if verifyErr != nil {
		testInstance.Fatalf("a token signed with the dev key must verify against the dev JWKS: %v", verifyErr)
	}
	if claims.WorkflowPath != ".github/workflows/deploy.yml" || claims.RepositoryID != 1001 {
		testInstance.Errorf("claims = %+v", claims)
	}
	if _, replayErr := verifier.Verify(context.Background(), token, replay); !errors.Is(replayErr, appidentity.ErrReplayed) {
		testInstance.Errorf("the same token again err = %v, want ErrReplayed", replayErr)
	}
}

// TestDeliveryAPIMountsTheDevOIDCKeySet checks the running container sees the very JWKS on the
// host, and started in dev mode.
func TestDeliveryAPIMountsTheDevOIDCKeySet(testInstance *testing.T) {
	waitForReady(testInstance)
	containerID, resolveErr := resolveServiceContainer(execRunner, "NOTIFS_DELIVERY_CONTAINER", "delivery-api")
	if resolveErr != nil {
		testInstance.Fatalf("resolve delivery-api container: %v", resolveErr)
	}
	commandCtx, cancelCommand := context.WithTimeout(context.Background(), dockerCommandLimit)
	defer cancelCommand()

	mounted, catErr := exec.CommandContext(commandCtx, "docker", "exec", containerID, "cat", containerJWKSPath).Output()
	if catErr != nil {
		testInstance.Fatalf("read %s in the delivery-api container (was .local/dev-oidc generated before make local-up?): %v",
			containerJWKSPath, catErr)
	}
	onHost, readErr := os.ReadFile(filepath.Join(devOIDCDir(testInstance), "jwks.json"))
	if readErr != nil {
		testInstance.Fatalf("read the host jwks: %v", readErr)
	}
	if !jsonEqual(testInstance, mounted, onHost) {
		testInstance.Errorf("the container's JWKS differs from .local/dev-oidc/jwks.json: it mounted a stale key set, rerun make local-up\ncontainer: %s\nhost: %s", mounted, onHost)
	}

	logs, logsErr := exec.CommandContext(commandCtx, "docker", "logs", containerID).CombinedOutput()
	if logsErr != nil {
		testInstance.Fatalf("docker logs: %v", logsErr)
	}
	if !strings.Contains(string(logs), `"oidc_dev_mode":true`) || !strings.Contains(string(logs), `"oidc_static_jwks":true`) {
		testInstance.Errorf("delivery-api did not log app identity configured in dev mode with a static JWKS:\n%s", logs)
	}
}

func jsonEqual(testInstance *testing.T, left []byte, right []byte) bool {
	testInstance.Helper()
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		testInstance.Fatalf("decode json: %v", err)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		testInstance.Fatalf("decode json: %v", err)
	}
	leftCanonical, _ := json.Marshal(leftValue)
	rightCanonical, _ := json.Marshal(rightValue)
	return string(leftCanonical) == string(rightCanonical)
}
