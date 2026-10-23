package registry

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	// resolveTimeout bounds the registration-time DNS lookup of an app-supplied URL host.
	resolveTimeout = 3 * time.Second

	// Field names echoed in registration-time URL errors.
	webhookURLField     = "secrets.url"
	discordWebhookField = "secrets.webhook_url"

	// lookupNetwork asks the resolver for both IPv4 and IPv6 addresses.
	lookupNetwork = "ip"
)

// HostResolver resolves a host name to its IP addresses. *net.Resolver satisfies it; tests inject a fake.
type HostResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// URLChecker is the registration-time SSRF check for app-supplied destination URLs. It resolves the URL's
// host itself and requires every resolved address to pass transport.IsPublicAddr. It never dials the URL, and
// the dial-time guard in the transport package stays as the second line of defence.
type URLChecker struct {
	resolver HostResolver
	timeout  time.Duration
	logger   *slog.Logger
}

// NewURLChecker returns a URLChecker. A nil resolver means net.DefaultResolver and a nil logger means
// slog.Default().
func NewURLChecker(resolver HostResolver, logger *slog.Logger) *URLChecker {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &URLChecker{resolver: resolver, timeout: resolveTimeout, logger: logger}
}

// CheckDestinationSecrets runs the registration-time check on the app-supplied URL in secrets for the given
// destination type: secrets.url for webhook and secrets.webhook_url for discord. ntfy carries no URL and
// passes. Any other type is rejected fail-closed. Pass the full effective secrets (after merging a patch),
// and call it after ValidateDestinationFields so the URL is already statically valid; a missing or
// malformed URL is still refused here rather than waved through.
//
// It always resolves (there is no changed-flag), so app_service.go calls it only on an app-owned create, on a
// PATCH that changes the URL secret, and on every rotate of a webhook destination, always before any DB
// transaction or row lock, because a lookup can take up to resolveTimeout (3 s) of DNS. A cancelled or
// expired caller ctx is reported as "host could not be resolved" (fail closed).
func (checker *URLChecker) CheckDestinationSecrets(
	ctx context.Context,
	destinationType string,
	secrets map[string]string,
) error {
	switch destinationType {
	case TypeNtfy:
		return nil
	case TypeWebhook:
		return checker.checkURL(ctx, webhookURLField, secrets["url"])
	case TypeDiscord:
		return checker.checkURL(ctx, discordWebhookField, secrets["webhook_url"])
	default:
		return &ValidationError{Message: "invalid type"}
	}
}

// destinationURLSecretKey names the secrets key that holds the app-supplied URL of a destination type
// (url for webhook, webhook_url for discord), or "" for a type with no URL (ntfy) or an unknown type.
func destinationURLSecretKey(destinationType string) string {
	switch destinationType {
	case TypeWebhook:
		return "url"
	case TypeDiscord:
		return "webhook_url"
	default:
		return ""
	}
}

// checkURL refuses rawURL unless its host is, or resolves only to, public addresses. field names the secrets
// key in the returned messages.
func (checker *URLChecker) checkURL(ctx context.Context, field, rawURL string) error {
	parsed, ok := parseHTTPSURL(rawURL)
	if !ok {
		return &ValidationError{Message: "invalid " + field}
	}
	host := parsed.Hostname()

	// An IP literal needs no lookup: check it directly. A malformed literal falls through to the resolver,
	// which fails closed.
	if literal, err := netip.ParseAddr(host); err == nil {
		if !transport.IsPublicAddr(literal) {
			return &ValidationError{Message: field + " must resolve to public addresses"}
		}
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, checker.timeout)
	defer cancel()
	started := time.Now()
	addrs, err := checker.resolver.LookupNetIP(lookupCtx, lookupNetwork, host)
	if err != nil || len(addrs) == 0 {
		// Log the host only, never the full URL: a Discord webhook URL carries its token in the path.
		attrs := []any{
			slog.String("field", field),
			slog.String("host", host),
			slog.Int("addresses", len(addrs)),
			slog.Bool("timeout", errors.Is(err, context.DeadlineExceeded)),
			slog.Duration("duration", time.Since(started)),
		}
		if err != nil {
			attrs = append(attrs, slog.Any("error", err))
		}
		checker.logger.Warn("registration-time URL host did not resolve", attrs...)
		return &ValidationError{Message: field + " host could not be resolved"}
	}
	for _, addr := range addrs {
		if !transport.IsPublicAddr(addr) {
			checker.logger.Warn("registration-time URL host resolves to a non-public address",
				slog.String("field", field),
				slog.String("host", host),
				slog.Int("addresses", len(addrs)),
				slog.Duration("duration", time.Since(started)))
			return &ValidationError{Message: field + " must resolve to public addresses"}
		}
	}
	return nil
}
