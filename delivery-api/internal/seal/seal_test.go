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

// namedSecrets returns a resolveSecret func that answers per variable name
// (unknown names resolve to "") and records the names it was asked, in order.
func namedSecrets(values map[string]string, asked *[]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if asked != nil {
			*asked = append(*asked, name)
		}
		return values[name], nil
	}
}

// primaryOnlySecret yields value and err for DELIVERY_MASTER_KEY and "" (absent)
// for the previous key.
func primaryOnlySecret(value string, err error) func(string) (string, error) {
	return func(name string) (string, error) {
		if name == EnvMasterKeyPrevious {
			return "", nil
		}
		return value, err
	}
}

// mustPairSealer builds a sealer with testKeyHex as primary and
// otherTestKeyHex as previous.
func mustPairSealer(testInstance *testing.T) *Sealer {
	testInstance.Helper()
	primary, primaryErr := ParseHexKey(testKeyHex)
	previous, previousErr := ParseHexKey(otherTestKeyHex)
	if primaryErr != nil || previousErr != nil {
		testInstance.Fatalf("ParseHexKey: %v %v", primaryErr, previousErr)
	}
	sealer, newErr := NewWithPrevious(primary, previous)
	if newErr != nil {
		testInstance.Fatalf("NewWithPrevious: %v", newErr)
	}
	return sealer
}

func TestOpenReportingKeyReportsWhichKeyOpened(testInstance *testing.T) {
	aad := []byte("aad")
	oldOnly := mustSealer(testInstance, otherTestKeyHex)
	newOnly := mustSealer(testInstance, testKeyHex)
	dual := mustPairSealer(testInstance)

	if !dual.HasPrevious() || newOnly.HasPrevious() {
		testInstance.Fatal("HasPrevious wrong")
	}

	oldSealed, _ := oldOnly.Seal([]byte("old"), aad)
	plain, used, openErr := dual.OpenReportingKey(oldSealed, aad)
	if openErr != nil || used != KeyPrevious || string(plain) != "old" {
		testInstance.Errorf("previous: (%q, %v, %v)", plain, used, openErr)
	}

	newSealed, _ := newOnly.Seal([]byte("new"), aad)
	plain, used, openErr = dual.OpenReportingKey(newSealed, aad)
	if openErr != nil || used != KeyPrimary || string(plain) != "new" {
		testInstance.Errorf("primary: (%q, %v, %v)", plain, used, openErr)
	}

	if _, openErr = dual.Open(oldSealed, aad); openErr != nil {
		testInstance.Errorf("Open must fall back to previous: %v", openErr)
	}
}

func TestDualSealerSealsUnderPrimaryOnly(testInstance *testing.T) {
	aad := []byte("aad")
	dual := mustPairSealer(testInstance)
	sealed, sealErr := dual.Seal([]byte("x"), aad)
	if sealErr != nil {
		testInstance.Fatal(sealErr)
	}
	if _, openErr := mustSealer(testInstance, testKeyHex).Open(sealed, aad); openErr != nil {
		testInstance.Errorf("primary-only open: %v", openErr)
	}
	if _, openErr := mustSealer(testInstance, otherTestKeyHex).Open(sealed, aad); !errors.Is(openErr, ErrDecrypt) {
		testInstance.Errorf("old-only open = %v, want ErrDecrypt", openErr)
	}
}

func TestDualSealerFailureModes(testInstance *testing.T) {
	aad := []byte("aad")
	third := strings.Repeat("ab", KeySize)
	dual := mustPairSealer(testInstance)
	thirdSealed, _ := mustSealer(testInstance, third).Seal([]byte("x"), aad)

	if _, used, openErr := dual.OpenReportingKey(thirdSealed, aad); !errors.Is(openErr, ErrDecrypt) || used != KeyNone {
		testInstance.Errorf("wrong key = (%v, %v)", used, openErr)
	}
	oldSealed, _ := mustSealer(testInstance, otherTestKeyHex).Seal([]byte("x"), aad)
	if _, _, openErr := dual.OpenReportingKey(oldSealed, []byte("other")); !errors.Is(openErr, ErrDecrypt) {
		testInstance.Errorf("wrong aad = %v", openErr)
	}
	if _, _, openErr := dual.OpenReportingKey([]byte{1}, aad); !errors.Is(openErr, ErrMalformed) {
		testInstance.Errorf("malformed = %v", openErr)
	}
	badVersion := bytes.Clone(oldSealed)
	badVersion[0] = 0x02
	if _, _, openErr := dual.OpenReportingKey(badVersion, aad); !errors.Is(openErr, ErrUnsupportedVersion) {
		testInstance.Errorf("version = %v", openErr)
	}
}

func TestPrimaryOnlyDropsPrevious(testInstance *testing.T) {
	aad := []byte("aad")
	dual := mustPairSealer(testInstance)
	view := dual.PrimaryOnly()
	if view.HasPrevious() {
		testInstance.Error("PrimaryOnly still has previous")
	}
	oldSealed, _ := mustSealer(testInstance, otherTestKeyHex).Seal([]byte("x"), aad)
	if _, openErr := view.Open(oldSealed, aad); !errors.Is(openErr, ErrDecrypt) {
		testInstance.Errorf("PrimaryOnly open of old = %v, want ErrDecrypt", openErr)
	}
	newSealed, _ := dual.Seal([]byte("x"), aad)
	if _, openErr := view.Open(newSealed, aad); openErr != nil {
		testInstance.Errorf("PrimaryOnly open of new: %v", openErr)
	}
}

func TestNewWithPreviousValidation(testInstance *testing.T) {
	key := make([]byte, KeySize)
	other := bytes.Repeat([]byte{7}, KeySize)
	for name, pair := range map[string][2][]byte{
		"short primary":  {key[:16], other},
		"short previous": {key, other[:16]},
		"nil previous":   {key, nil},
		"equal":          {key, bytes.Clone(key)},
	} {
		sealer, newErr := NewWithPrevious(pair[0], pair[1])
		if newErr == nil || sealer != nil {
			testInstance.Errorf("%s: want error, got (%v, %v)", name, sealer, newErr)
		}
	}
}

func TestFromEnvPreviousKey(testInstance *testing.T) {
	var asked []string
	sealer, fromEnvErr := FromEnv(namedSecrets(map[string]string{
		EnvMasterKey: testKeyHex, EnvMasterKeyPrevious: otherTestKeyHex,
	}, &asked))
	if fromEnvErr != nil || !sealer.HasPrevious() {
		testInstance.Fatalf("FromEnv = (%v, %v)", sealer, fromEnvErr)
	}
	if len(asked) != 2 || asked[0] != EnvMasterKey || asked[1] != EnvMasterKeyPrevious {
		testInstance.Errorf("asked = %v", asked)
	}

	single, singleErr := FromEnv(namedSecrets(map[string]string{EnvMasterKey: testKeyHex}, nil))
	if singleErr != nil || single.HasPrevious() {
		testInstance.Errorf("unset previous = (%v, %v)", single, singleErr)
	}
}

func TestFromEnvPreviousKeyErrors(testInstance *testing.T) {
	resolverFailure := errors.New("unreadable file")
	testCases := []struct {
		name     string
		previous string
		resolver func(string) (string, error)
		want     string
	}{
		{name: "malformed", previous: "zz-SECRETMARKER", want: "DELIVERY_MASTER_KEY_PREVIOUS must be 64 lowercase hex characters"},
		{name: "wrong length", previous: otherTestKeyHex[:60], want: "DELIVERY_MASTER_KEY_PREVIOUS must be 64 lowercase hex characters"},
		{name: "uppercase", previous: strings.ToUpper(otherTestKeyHex), want: "DELIVERY_MASTER_KEY_PREVIOUS must be 64 lowercase hex characters"},
		{name: "whitespace-only plain env", previous: " \n", want: "DELIVERY_MASTER_KEY_PREVIOUS must be 64 lowercase hex characters"},
		{name: "equal to primary", previous: testKeyHex, want: "DELIVERY_MASTER_KEY_PREVIOUS must differ from DELIVERY_MASTER_KEY"},
		{
			name: "resolver error",
			resolver: func(name string) (string, error) {
				if name == EnvMasterKeyPrevious {
					return "", resolverFailure
				}
				return testKeyHex, nil
			},
			want: EnvMasterKeyPrevious,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			resolver := testCase.resolver
			if resolver == nil {
				resolver = namedSecrets(map[string]string{
					EnvMasterKey: testKeyHex, EnvMasterKeyPrevious: testCase.previous,
				}, nil)
			}
			sealer, fromEnvErr := FromEnv(resolver)
			if fromEnvErr == nil || sealer != nil {
				subTest.Fatalf("FromEnv = (%v, %v), want error", sealer, fromEnvErr)
			}
			message := fromEnvErr.Error()
			if !strings.Contains(message, testCase.want) {
				subTest.Errorf("error %q lacks %q", message, testCase.want)
			}
			if testCase.resolver != nil && !errors.Is(fromEnvErr, resolverFailure) {
				subTest.Error("resolver error not wrapped")
			}
			lower := strings.ToLower(message)
			for _, secret := range []string{testKeyHex, otherTestKeyHex, "secretmarker"} {
				if strings.Contains(lower, secret) {
					subTest.Errorf("error leaks key material: %q", message)
				}
			}
		})
	}
}

func TestFromEnvWhitespaceOnlyPreviousFileIsAbsent(testInstance *testing.T) {
	// secretenv.Resolve trims file contents, so a whitespace-only
	// DELIVERY_MASTER_KEY_PREVIOUS_FILE reaches FromEnv as "".
	sealer, fromEnvErr := FromEnv(namedSecrets(map[string]string{
		EnvMasterKey: testKeyHex, EnvMasterKeyPrevious: strings.TrimSpace(" \n"),
	}, nil))
	if fromEnvErr != nil || sealer.HasPrevious() {
		testInstance.Fatalf("FromEnv = (%v, %v), want single-key sealer", sealer, fromEnvErr)
	}
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
			sealer, fromEnvErr := FromEnv(primaryOnlySecret(testCase.value, testCase.resolveErr))
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
	var asked []string
	resolver := namedSecrets(map[string]string{EnvMasterKey: testKeyHex}, &asked)

	if _, fromEnvErr := FromEnv(resolver); fromEnvErr != nil {
		testInstance.Fatalf("FromEnv error = %v, want nil", fromEnvErr)
	}
	if len(asked) != 2 || asked[0] != EnvMasterKey || asked[1] != EnvMasterKeyPrevious {
		testInstance.Fatalf("resolver asked = %v, want [%s %s]", asked, EnvMasterKey, EnvMasterKeyPrevious)
	}
}
