package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/enforcement"
)

const (
	internalCredentialEnv     = "PROVISIONING_INTERNAL_CREDENTIAL"
	internalCredentialModeEnv = "PROVISIONING_INTERNAL_CREDENTIAL_MODE"
)

// errEnforceWithoutCredential is returned when enforce mode is selected but no
// credential was loaded: every request would be rejected, so refuse to start.
var errEnforceWithoutCredential = errors.New(
	internalCredentialModeEnv + " is enforce but " + internalCredentialEnv + " is empty; " +
		"set the credential or set the mode to log-only explicitly")

// resolveGrantMode parses the raw PROVISIONING_APP_GRANT_MODE: empty means
// enforce (fail closed), "log-only" is the explicit rollout setting, and
// anything else is an error the caller must treat as fatal.
func resolveGrantMode(modeValue string) (enforcement.Mode, error) {
	mode, parseErr := enforcement.Parse(appGrantModeEnv, modeValue)
	if parseErr != nil {
		return enforcement.Enforce, fmt.Errorf("invalid app grant mode: %w", parseErr)
	}
	return mode, nil
}

// resolveCredentialMode validates the internal-credential startup settings and
// returns the mode to run in plus the credential to configure. modeValue is the
// raw PROVISIONING_INTERNAL_CREDENTIAL_MODE (empty means enforce, fail closed)
// and rawCredential is the resolved secret as read, which is trimmed here so a
// trailing newline in a hand-written secret file cannot reject every enforced
// request. An invalid mode, or enforce mode with an empty (or whitespace-only)
// credential, is an error the caller must treat as fatal. Log-only mode with an
// empty credential logs exactly one startup WARN and still starts. The
// credential value is never logged or placed in an error.
func resolveCredentialMode(logger *slog.Logger, modeValue string, rawCredential string) (enforcement.Mode, string, error) {
	mode, parseErr := enforcement.Parse(internalCredentialModeEnv, modeValue)
	if parseErr != nil {
		return enforcement.Enforce, "", fmt.Errorf("invalid internal credential mode: %w", parseErr)
	}
	credential := strings.TrimSpace(rawCredential)
	if credential == "" {
		if mode == enforcement.Enforce {
			return enforcement.Enforce, "", errEnforceWithoutCredential
		}
		logger.Warn("internal credential is not configured; every request will be logged as would-reject",
			"mode", mode.String(), "env", internalCredentialEnv)
	}
	return mode, credential, nil
}
