package secretenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	testCases := []struct {
		name string
		// envValue, if non-empty, is set as the plain <key> env var.
		envValue string
		// fileContents, if non-nil, is written to a temp file whose path is
		// set as the <key>_FILE env var.
		fileContents *string
		want         string
		wantErr      bool
	}{
		{
			name: "neither set returns empty",
			want: "",
		},
		{
			name:     "plain env only",
			envValue: "plain-value",
			want:     "plain-value",
		},
		{
			name:         "file indirection is read and trailing whitespace trimmed",
			fileContents: strptr("file-secret\n"),
			want:         "file-secret",
		},
		{
			name:         "file indirection wins over plain env",
			envValue:     "plain-value",
			fileContents: strptr("file-secret\n"),
			want:         "file-secret",
		},
		{
			name:         "empty file yields empty value (dual-write stays disabled)",
			fileContents: strptr("\n"),
			want:         "",
		},
	}

	const key = "TEST_SECRET"
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.envValue != "" {
				t.Setenv(key, testCase.envValue)
			}
			if testCase.fileContents != nil {
				path := filepath.Join(t.TempDir(), "secret")
				if writeErr := os.WriteFile(path, []byte(*testCase.fileContents), 0o600); writeErr != nil {
					t.Fatalf("write temp secret: %v", writeErr)
				}
				t.Setenv(key+"_FILE", path)
			}

			got, err := Resolve(key)
			if testCase.wantErr && err == nil {
				t.Fatalf("Resolve(%q) = %q, want error", key, got)
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("Resolve(%q) unexpected error: %v", key, err)
			}
			if got != testCase.want {
				t.Fatalf("Resolve(%q) = %q, want %q", key, got, testCase.want)
			}
		})
	}
}

func TestResolveMissingFileErrors(t *testing.T) {
	const key = "TEST_SECRET"
	t.Setenv(key+"_FILE", filepath.Join(t.TempDir(), "does-not-exist"))

	got, err := Resolve(key)
	if err == nil {
		t.Fatalf("Resolve(%q) = %q, want error for unreadable file", key, got)
	}
	if got != "" {
		t.Fatalf("Resolve(%q) = %q on error, want empty string", key, got)
	}
}

func strptr(value string) *string {
	return &value
}
