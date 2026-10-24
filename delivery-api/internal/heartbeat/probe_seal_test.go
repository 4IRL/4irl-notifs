package heartbeat

import (
	"errors"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

func TestOpenProbeAccessTokenFailsClosed(testInstance *testing.T) {
	sealer, sealerErr := seal.New([]byte("0123456789abcdef0123456789abcdef"))
	if sealerErr != nil {
		testInstance.Fatalf("seal.New: %v", sealerErr)
	}
	const checkID int64 = 7
	sealPlaintext := func(plaintext string) []byte {
		testInstance.Helper()
		sealed, err := sealer.Seal([]byte(plaintext), probeAccessAAD(checkID))
		if err != nil {
			testInstance.Fatalf("seal: %v", err)
		}
		return sealed
	}

	scenarios := []struct {
		name   string
		sealer *seal.Sealer
		sealed []byte
		want   error
	}{
		{"nil sealer", nil, []byte("anything"), errNoSealer},
		{"invalid json", sealer, sealPlaintext("not json"), errMalformedAccessToken},
		{"empty client id", sealer, sealPlaintext(`{"client_id":"","client_secret":"secret"}`), errMalformedAccessToken},
		{"empty client secret", sealer, sealPlaintext(`{"client_id":"id","client_secret":""}`), errMalformedAccessToken},
		{"missing fields", sealer, sealPlaintext(`{}`), errMalformedAccessToken},
	}
	for _, scenario := range scenarios {
		testInstance.Run(scenario.name, func(subTest *testing.T) {
			token, err := OpenProbeAccessToken(scenario.sealer, checkID, scenario.sealed)
			if !errors.Is(err, scenario.want) {
				subTest.Fatalf("error = %v, want %v", err, scenario.want)
			}
			if token != (AccessToken{}) {
				subTest.Fatalf("token = %+v on error, want zero", token)
			}
		})
	}
}

func TestSealProbeAccessTokenWithoutASealerFailsClosed(testInstance *testing.T) {
	sealed, err := sealProbeAccessToken(nil, 7, AccessToken{ClientID: "id", ClientSecret: "secret"})
	if !errors.Is(err, errNoSealer) || sealed != nil {
		testInstance.Fatalf("sealed = %v, err = %v; want nil and errNoSealer", sealed, err)
	}
}
