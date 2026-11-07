package personal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const secretsAADPrefix = "delivery-api/personal-destination/v1:"

// errNoSealer means secrets were sealed or opened without a sealer. The store
// fails closed rather than keep them in the clear.
var errNoSealer = errors.New("personal: no sealer configured")

// SecretsAAD binds a sealed blob to its row id, so it cannot be copied onto
// another row. The key rotation tool shares this derivation.
func SecretsAAD(id int64) []byte {
	return []byte(secretsAADPrefix + strconv.FormatInt(id, 10))
}

// SealSecrets JSON-encodes secrets and seals them for the row with that id.
// Errors never include the secrets.
func SealSecrets(sealer *seal.Sealer, id int64, secrets map[string]string) ([]byte, error) {
	if sealer == nil {
		return nil, errNoSealer
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	plaintext, marshalErr := json.Marshal(secrets)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode secrets of personal destination %d", id)
	}
	sealed, sealErr := sealer.Seal(plaintext, SecretsAAD(id))
	if sealErr != nil {
		return nil, fmt.Errorf("seal secrets of personal destination %d: %w", id, sealErr)
	}
	return sealed, nil
}

// OpenSecrets is the single unseal path. A blob sealed for another row id fails
// with the seal package's decrypt error.
func OpenSecrets(sealer *seal.Sealer, id int64, sealed []byte) (map[string]string, error) {
	if sealer == nil {
		return nil, errNoSealer
	}
	plaintext, openErr := sealer.Open(sealed, SecretsAAD(id))
	if openErr != nil {
		return nil, fmt.Errorf("open secrets of personal destination %d: %w", id, openErr)
	}
	var secrets map[string]string
	if unmarshalErr := json.Unmarshal(plaintext, &secrets); unmarshalErr != nil {
		return nil, fmt.Errorf("decode secrets of personal destination %d", id)
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	return secrets, nil
}
