package seal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const (
	testKeyHex      = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	otherTestKeyHex = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
	minSealedLength = 1 + nonceSize + tagSize
)

func mustSealer(testInstance *testing.T, keyHex string) *Sealer {
	testInstance.Helper()
	key, parseErr := ParseHexKey(keyHex)
	if parseErr != nil {
		testInstance.Fatalf("ParseHexKey: %v", parseErr)
	}
	sealer, newErr := New(key)
	if newErr != nil {
		testInstance.Fatalf("New: %v", newErr)
	}
	return sealer
}

func TestSealOpenRoundTrip(testInstance *testing.T) {
	sealer := mustSealer(testInstance, testKeyHex)
	aad := []byte("destination-name")

	testCases := []struct {
		name      string
		plaintext []byte
	}{
		{name: "empty", plaintext: []byte{}},
		{name: "ascii", plaintext: []byte(`{"token":"abc123"}`)},
		{name: "multi-byte utf-8", plaintext: []byte("héllo 世界 🔐")},
		{name: "64 KiB", plaintext: bytes.Repeat([]byte{0xA5}, 64*1024)},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			sealed, sealErr := sealer.Seal(testCase.plaintext, aad)
			if sealErr != nil {
				subTest.Fatalf("Seal: %v", sealErr)
			}
			if sealed[0] != formatVersion {
				subTest.Errorf("version byte = %#x, want %#x", sealed[0], formatVersion)
			}
			if want := minSealedLength + len(testCase.plaintext); len(sealed) != want {
				subTest.Errorf("sealed length = %d, want %d", len(sealed), want)
			}

			opened, openErr := sealer.Open(sealed, aad)
			if openErr != nil {
				subTest.Fatalf("Open: %v", openErr)
			}
			if !bytes.Equal(opened, testCase.plaintext) {
				subTest.Errorf("round trip mismatch")
			}
		})
	}
}

func TestSealUsesRandomNonce(testInstance *testing.T) {
	sealer := mustSealer(testInstance, testKeyHex)
	plaintext := []byte("same plaintext")
	aad := []byte("aad")

	const sealCount = 64
	seenNonces := make(map[string]struct{}, sealCount)
	for range sealCount {
		sealed, sealErr := sealer.Seal(plaintext, aad)
		if sealErr != nil {
			testInstance.Fatalf("Seal: %v", sealErr)
		}
		nonce := string(sealed[1 : 1+nonceSize])
		if _, reused := seenNonces[nonce]; reused {
			testInstance.Fatalf("nonce %x was reused across %d seals", nonce, sealCount)
		}
		seenNonces[nonce] = struct{}{}
	}
}

func TestOpenDetectsTampering(testInstance *testing.T) {
	sealer := mustSealer(testInstance, testKeyHex)
	plaintext := []byte("a secret worth protecting")
	aad := []byte("destination-name")

	sealed, sealErr := sealer.Seal(plaintext, aad)
	if sealErr != nil {
		testInstance.Fatalf("Seal: %v", sealErr)
	}

	testCases := []struct {
		name    string
		index   int
		wantErr error
	}{
		{name: "version byte", index: 0, wantErr: ErrUnsupportedVersion},
		{name: "nonce", index: 1, wantErr: ErrDecrypt},
		{name: "nonce last byte", index: nonceSize, wantErr: ErrDecrypt},
		{name: "ciphertext first byte", index: 1 + nonceSize, wantErr: ErrDecrypt},
		{name: "ciphertext last byte", index: len(sealed) - tagSize - 1, wantErr: ErrDecrypt},
		{name: "tag first byte", index: len(sealed) - tagSize, wantErr: ErrDecrypt},
		{name: "tag last byte", index: len(sealed) - 1, wantErr: ErrDecrypt},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			tampered := bytes.Clone(sealed)
			tampered[testCase.index] ^= 0x01

			opened, openErr := sealer.Open(tampered, aad)
			if !errors.Is(openErr, testCase.wantErr) {
				subTest.Errorf("Open error = %v, want %v", openErr, testCase.wantErr)
			}
			if opened != nil {
				subTest.Error("Open returned plaintext for tampered input")
			}
		})
	}
}

func TestOpenRejectsWrongKeyAndAAD(testInstance *testing.T) {
	sealer := mustSealer(testInstance, testKeyHex)
	otherSealer := mustSealer(testInstance, otherTestKeyHex)
	aad := []byte("destination-a")

	sealed, sealErr := sealer.Seal([]byte("secret"), aad)
	if sealErr != nil {
		testInstance.Fatalf("Seal: %v", sealErr)
	}

	testCases := []struct {
		name   string
		opener *Sealer
		aad    []byte
	}{
		{name: "wrong key", opener: otherSealer, aad: aad},
		{name: "wrong aad", opener: sealer, aad: []byte("destination-b")},
		{name: "missing aad", opener: sealer, aad: nil},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			opened, openErr := testCase.opener.Open(sealed, testCase.aad)
			if !errors.Is(openErr, ErrDecrypt) {
				subTest.Errorf("Open error = %v, want ErrDecrypt", openErr)
			}
			if opened != nil {
				subTest.Error("Open returned plaintext")
			}
		})
	}
}

func TestOpenRejectsMalformedInput(testInstance *testing.T) {
	sealer := mustSealer(testInstance, testKeyHex)

	testCases := []struct {
		name   string
		sealed []byte
	}{
		{name: "nil", sealed: nil},
		{name: "empty", sealed: []byte{}},
		{name: "one byte shorter than minimum", sealed: make([]byte, minSealedLength-1)},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			opened, openErr := sealer.Open(testCase.sealed, nil)
			if !errors.Is(openErr, ErrMalformed) {
				subTest.Errorf("Open error = %v, want ErrMalformed", openErr)
			}
			if opened != nil {
				subTest.Error("Open returned plaintext")
			}
		})
	}
}

func TestNewRejectsWrongKeyLength(testInstance *testing.T) {
	testCases := []struct {
		name    string
		key     []byte
		wantErr bool
	}{
		{name: "nil", key: nil, wantErr: true},
		{name: "empty", key: make([]byte, 0), wantErr: true},
		{name: "16 bytes", key: make([]byte, 16), wantErr: true},
		{name: "24 bytes", key: make([]byte, 24), wantErr: true},
		{name: "31 bytes", key: make([]byte, 31), wantErr: true},
		{name: "33 bytes", key: make([]byte, 33), wantErr: true},
		{name: "64 bytes", key: make([]byte, 64), wantErr: true},
		{name: "32 bytes", key: make([]byte, KeySize)},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			sealer, newErr := New(testCase.key)
			if testCase.wantErr {
				if newErr == nil {
					subTest.Fatalf("New(%d-byte key) succeeded, want error", len(testCase.key))
				}
				if sealer != nil {
					subTest.Error("New returned a sealer alongside an error")
				}
				return
			}
			if newErr != nil || sealer == nil {
				subTest.Fatalf("New(%d-byte key) = (%v, %v), want a sealer", len(testCase.key), sealer, newErr)
			}
		})
	}
}

func TestParseHexKey(testInstance *testing.T) {
	// testKeyHex decodes to the bytes 0x00, 0x01, ... 0x1f.
	wantKey := make([]byte, KeySize)
	for index := range wantKey {
		wantKey[index] = byte(index)
	}

	testCases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "64 lowercase hex", value: testKeyHex},
		{name: "63 chars", value: testKeyHex[:63], wantErr: true},
		{name: "65 chars", value: testKeyHex + "0", wantErr: true},
		{name: "uppercase", value: strings.ToUpper(otherTestKeyHex), wantErr: true},
		{name: "non-hex", value: strings.Repeat("g", 64), wantErr: true},
		{name: "empty", value: "", wantErr: true},
		{name: "surrounding whitespace", value: testKeyHex + "\n", wantErr: true},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			key, parseErr := ParseHexKey(testCase.value)
			if testCase.wantErr {
				if parseErr == nil {
					subTest.Fatalf("ParseHexKey(%q) succeeded, want error", testCase.value)
				}
				if key != nil {
					subTest.Error("ParseHexKey returned a key alongside an error")
				}
				return
			}
			if parseErr != nil {
				subTest.Fatalf("ParseHexKey: %v", parseErr)
			}
			if !bytes.Equal(key, wantKey) {
				subTest.Errorf("decoded key = %x, want %x", key, wantKey)
			}
		})
	}
}

// fixedSecret returns a resolveSecret func yielding value and err for any key.
func fixedSecret(value string, err error) func(string) (string, error) {
	return func(string) (string, error) { return value, err }
}

func TestFromEnv(testInstance *testing.T) {
	resolverFailure := errors.New("secret backend exploded")
	invalidValue := "not-a-valid-key-SUPERSECRETMARKER"

	testCases := []struct {
		name       string
		value      string
		resolveErr error
		wantErr    string
		wantWrap   error
	}{
		{name: "valid key", value: testKeyHex},
		{name: "missing key", value: "", wantErr: "DELIVERY_MASTER_KEY is required"},
		{
			name:    "invalid key",
			value:   invalidValue,
			wantErr: "DELIVERY_MASTER_KEY must be 64 lowercase hex characters",
		},
		{
			name:       "resolver error is wrapped",
			resolveErr: resolverFailure,
			wantErr:    "DELIVERY_MASTER_KEY",
			wantWrap:   resolverFailure,
		},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			sealer, fromEnvErr := FromEnv(fixedSecret(testCase.value, testCase.resolveErr))
			if testCase.wantErr == "" {
				if fromEnvErr != nil || sealer == nil {
					subTest.Fatalf("FromEnv = (%v, %v), want a sealer", sealer, fromEnvErr)
				}
				return
			}

			if fromEnvErr == nil {
				subTest.Fatal("FromEnv succeeded, want error")
			}
			if sealer != nil {
				subTest.Error("FromEnv returned a sealer alongside an error")
			}
			if !strings.Contains(fromEnvErr.Error(), testCase.wantErr) {
				subTest.Errorf("error %q does not contain %q", fromEnvErr, testCase.wantErr)
			}
			if testCase.wantWrap != nil && !errors.Is(fromEnvErr, testCase.wantWrap) {
				subTest.Errorf("error %v does not wrap %v", fromEnvErr, testCase.wantWrap)
			}
			if testCase.value != "" && strings.Contains(fromEnvErr.Error(), testCase.value) {
				subTest.Errorf("error leaks the key value: %q", fromEnvErr)
			}
		})
	}
}

func TestFromEnvDoesNotLeakInvalidKeyInError(testInstance *testing.T) {
	// 64 chars of uppercase hex: well-formed length, invalid case.
	leaky := strings.ToUpper(testKeyHex)

	_, fromEnvErr := FromEnv(fixedSecret(leaky, nil))
	if fromEnvErr == nil {
		testInstance.Fatal("FromEnv succeeded, want error")
	}
	if strings.Contains(fromEnvErr.Error(), leaky) {
		testInstance.Errorf("error leaks the key value: %q", fromEnvErr)
	}
	if strings.Contains(strings.ToLower(fromEnvErr.Error()), testKeyHex) {
		testInstance.Errorf("error leaks the lowercase-folded key value: %q", fromEnvErr)
	}
}

func TestFromEnvAsksResolverForMasterKey(testInstance *testing.T) {
	var gotKey string
	resolver := func(key string) (string, error) {
		gotKey = key
		return testKeyHex, nil
	}

	if _, fromEnvErr := FromEnv(resolver); fromEnvErr != nil {
		testInstance.Fatalf("FromEnv error = %v, want nil", fromEnvErr)
	}
	if gotKey != EnvMasterKey {
		testInstance.Fatalf("resolver key = %q, want %q", gotKey, EnvMasterKey)
	}
}
