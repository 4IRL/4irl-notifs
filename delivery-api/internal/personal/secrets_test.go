package personal

import (
	"bytes"
	"errors"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

func testSealer(testInstance *testing.T) *seal.Sealer {
	testInstance.Helper()
	sealer, newErr := seal.New(bytes.Repeat([]byte{0x42}, seal.KeySize))
	if newErr != nil {
		testInstance.Fatalf("seal.New: %v", newErr)
	}
	return sealer
}

func TestSecretsAAD(testInstance *testing.T) {
	if got := string(SecretsAAD(12)); got != "delivery-api/personal-destination/v1:12" {
		testInstance.Fatalf("SecretsAAD(12) = %q", got)
	}
}

func TestSealOpenSecretsRoundTrip(testInstance *testing.T) {
	sealer := testSealer(testInstance)
	secrets := map[string]string{"url": "https://discord.com/api/webhooks/1/abc"}
	sealed, sealErr := SealSecrets(sealer, 5, secrets)
	if sealErr != nil {
		testInstance.Fatalf("SealSecrets: %v", sealErr)
	}
	opened, openErr := OpenSecrets(sealer, 5, sealed)
	if openErr != nil || opened["url"] != secrets["url"] {
		testInstance.Fatalf("OpenSecrets = (%v, %v), want %v", opened, openErr, secrets)
	}
}

func TestSecretsSealedForOneRowCannotBeOpenedAsAnother(testInstance *testing.T) {
	sealer := testSealer(testInstance)
	sealed, sealErr := SealSecrets(sealer, 5, map[string]string{"url": "https://example.com/hook"})
	if sealErr != nil {
		testInstance.Fatalf("SealSecrets: %v", sealErr)
	}
	_, openErr := OpenSecrets(sealer, 6, sealed)
	if !errors.Is(openErr, seal.ErrDecrypt) {
		testInstance.Fatalf("OpenSecrets as row 6 error = %v, want seal.ErrDecrypt", openErr)
	}
}

func TestSecretsFailClosedWithoutSealer(testInstance *testing.T) {
	if _, sealErr := SealSecrets(nil, 1, nil); sealErr == nil {
		testInstance.Error("SealSecrets with no sealer succeeded")
	}
	if _, openErr := OpenSecrets(nil, 1, []byte{1}); openErr == nil {
		testInstance.Error("OpenSecrets with no sealer succeeded")
	}
}
