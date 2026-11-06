// Package envint parses bounded integer settings from the environment.
package envint

import (
	"fmt"
	"strconv"
)

// Bounded reads the setting name through getenv. An empty value gives fallback;
// anything else must be an integer from 1 to limit. The error text never echoes
// the value.
func Bounded(getenv func(string) string, name string, fallback, limit int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, parseErr := strconv.Atoi(raw)
	if parseErr != nil || value < 1 || value > limit {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return value, nil
}
