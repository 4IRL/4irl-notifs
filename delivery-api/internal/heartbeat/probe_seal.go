package heartbeat

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const probeAccessAADPrefix = "delivery-api/heartbeat-probe/v1:"

var (
	// errNoSealer means a probe credential was sealed or opened by a store built
	// without a sealer. The store fails closed rather than keep it in the clear.
	errNoSealer = errors.New("heartbeat: no sealer configured for probe access token")
	// errMalformedAccessToken means the unsealed plaintext is not a usable token.
	errMalformedAccessToken = errors.New("heartbeat: malformed access token")
)

// probeAccessPlaintext is the JSON the sealed bytes hold.
type probeAccessPlaintext struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// ProbeAccessAAD binds a sealed token to its check, so it cannot be copied onto another one.
// The key rotation tool shares this derivation.
func ProbeAccessAAD(checkID int64) []byte {
	return []byte(probeAccessAADPrefix + strconv.FormatInt(checkID, 10))
}

// sealProbeAccessToken seals token for the check with that id. Errors never include the token.
func sealProbeAccessToken(sealer *seal.Sealer, checkID int64, token AccessToken) ([]byte, error) {
	if sealer == nil {
		return nil, errNoSealer
	}
	plaintext, marshalErr := json.Marshal(probeAccessPlaintext(token))
	if marshalErr != nil {
		return nil, errMalformedAccessToken
	}
	return sealer.Seal(plaintext, ProbeAccessAAD(checkID))
}

// OpenProbeAccessToken is the single unseal path for a probe check's Access token. A wrong check id
// fails with seal.ErrDecrypt. Its errors wrap only a seal sentinel or a fixed malformed-token error.
func OpenProbeAccessToken(sealer *seal.Sealer, checkID int64, sealed []byte) (AccessToken, error) {
	if sealer == nil {
		return AccessToken{}, errNoSealer
	}
	plaintext, openErr := sealer.Open(sealed, ProbeAccessAAD(checkID))
	if openErr != nil {
		return AccessToken{}, openErr
	}
	var decoded probeAccessPlaintext
	if unmarshalErr := json.Unmarshal(plaintext, &decoded); unmarshalErr != nil {
		return AccessToken{}, errMalformedAccessToken
	}
	if decoded.ClientID == "" || decoded.ClientSecret == "" {
		return AccessToken{}, errMalformedAccessToken
	}
	return AccessToken(decoded), nil
}
