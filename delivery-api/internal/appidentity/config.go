package appidentity

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

const (
	// AudienceEnvVar names the optional environment variable holding the OIDC audience a
	// GitHub token must be minted for.
	AudienceEnvVar = "DELIVERY_OIDC_AUDIENCE"
	// JWKSFileEnvVar names the dev-only environment variable pointing the verifier at a
	// static JWKS file instead of GitHub's. It is honoured only with DevModeEnvVar set.
	JWKSFileEnvVar = "DELIVERY_OIDC_JWKS_FILE"
	// DevModeEnvVar is the dev-only switch ("1") that allows JWKSFileEnvVar. Production
	// never sets it (docker-compose.prod.yml sets neither dev variable).
	DevModeEnvVar = "DELIVERY_OIDC_DEV_MODE"
	// PendingRequestDaysEnvVar names the optional environment variable that sets how many days
	// a pending registration request may sit untouched before the periodic cleanup deletes it.
	PendingRequestDaysEnvVar = "DELIVERY_PENDING_IDENTITY_REQUEST_DAYS"

	// DefaultAudience is the audience used when AudienceEnvVar is unset. The reusable deploy
	// action requests tokens for the same default.
	DefaultAudience = "notifs-delivery"
	// DefaultPendingRequestDays is the pending-request TTL when PendingRequestDaysEnvVar is unset.
	DefaultPendingRequestDays = 7
	// MaxPendingRequestDays is the largest accepted PendingRequestDaysEnvVar.
	MaxPendingRequestDays = 365

	maxAudienceChars = 200
	hoursPerDay      = 24
)

// audiencePattern accepts printable, non-space ASCII, so the value is safe in a URL query
// and a log line.
var audiencePattern = regexp.MustCompile(fmt.Sprintf(`^[\x21-\x7e]{1,%d}$`, maxAudienceChars))

// Config is the OIDC verifier's environment configuration.
type Config struct {
	// Audience is the aud a token must carry.
	Audience string
	// JWKSFile, when set, is a static JWKS used instead of GitHub's. Only ever set together
	// with DevMode.
	JWKSFile string
	// DevMode records that the operator explicitly opted into dev-only behaviour.
	DevMode bool
	// PendingRequestTTL is how long a pending registration request may go without a refresh
	// (a repeat exchange with the same claims) before the periodic cleanup deletes it. It is
	// always positive.
	PendingRequestTTL time.Duration
}

// ConfigFromEnv reads the OIDC configuration through getenv. The audience defaults to
// DefaultAudience. DELIVERY_OIDC_DEV_MODE must be empty, "0" or "1". Setting
// DELIVERY_OIDC_JWKS_FILE without DELIVERY_OIDC_DEV_MODE=1 is an error, so a production
// stack can never silently trust a file-supplied key set. DELIVERY_PENDING_IDENTITY_REQUEST_DAYS
// is an integer from 1 to MaxPendingRequestDays and defaults to DefaultPendingRequestDays; any
// other value is an error rather than a silent fallback. Error text never echoes a value.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	config := Config{
		Audience:          DefaultAudience,
		PendingRequestTTL: DefaultPendingRequestDays * hoursPerDay * time.Hour,
	}

	if rawDays := getenv(PendingRequestDaysEnvVar); rawDays != "" {
		days, parseErr := strconv.Atoi(rawDays)
		if parseErr != nil || days < 1 || days > MaxPendingRequestDays {
			return Config{}, fmt.Errorf("invalid %s", PendingRequestDaysEnvVar)
		}
		config.PendingRequestTTL = time.Duration(days) * hoursPerDay * time.Hour
	}

	if audience := getenv(AudienceEnvVar); audience != "" {
		if !audiencePattern.MatchString(audience) {
			return Config{}, fmt.Errorf("invalid %s", AudienceEnvVar)
		}
		config.Audience = audience
	}

	switch getenv(DevModeEnvVar) {
	case "", "0":
		config.DevMode = false
	case "1":
		config.DevMode = true
	default:
		return Config{}, fmt.Errorf("invalid %s: must be empty, 0 or 1", DevModeEnvVar)
	}

	config.JWKSFile = getenv(JWKSFileEnvVar)
	if config.JWKSFile != "" && !config.DevMode {
		return Config{}, errors.New(JWKSFileEnvVar + " requires " + DevModeEnvVar + "=1")
	}
	return config, nil
}
