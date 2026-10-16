// Package secretenv resolves a secret from KEY_FILE (a Docker secret file, which keeps the
// value out of the container env) or, failing that, from the plain KEY env var.
package secretenv

import (
	"fmt"
	"os"
	"strings"
)

// fileSuffix is appended to a key to form the name of the env var that holds
// the path to the secret's file indirection.
const fileSuffix = "_FILE"

// Resolve returns the value of the secret named key. If <key>_FILE is set, the
// file it names is read and its whitespace-trimmed contents are returned; a
// set-but-unreadable <key>_FILE yields an error (and an empty string) so the
// caller can decide whether to fail or degrade. Otherwise the plain <key> env
// var is returned. An empty file (or unset key) resolves to "".
func Resolve(key string) (string, error) {
	if filePath := os.Getenv(key + fileSuffix); filePath != "" {
		contents, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return "", fmt.Errorf("read secret file for %s: %w", key, readErr)
		}
		return strings.TrimSpace(string(contents)), nil
	}
	return os.Getenv(key), nil
}
