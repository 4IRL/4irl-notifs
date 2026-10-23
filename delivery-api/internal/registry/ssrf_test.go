package registry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeResolver is a scripted HostResolver. Hosts in answers resolve to the listed addresses, hosts in failures
// return the listed error (together with any addresses listed for the host in partial), hosts in blocking wait
// for the lookup context to end, and any other host is NXDOMAIN. It records every lookup and the deadline each
// lookup context carried.
type fakeResolver struct {
	mutex     sync.Mutex
	answers   map[string][]netip.Addr
	failures  map[string]error
	partial   map[string][]netip.Addr
	blocking  map[string]bool
	lookups   []string
	deadlines []time.Duration
}

var errNXDomain = errors.New("lookup: no such host")

func (resolver *fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	resolver.mutex.Lock()
	resolver.lookups = append(resolver.lookups, host)
	if deadline, ok := ctx.Deadline(); ok {
		resolver.deadlines = append(resolver.deadlines, time.Until(deadline))
	}
	resolver.mutex.Unlock()
	if network != "ip" {
		return nil, errors.New("unexpected network " + network)
	}
	if resolver.blocking[host] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err, failed := resolver.failures[host]; failed {
		return resolver.partial[host], err
	}
	if addrs, found := resolver.answers[host]; found {
		return addrs, nil
	}
	return nil, errNXDomain
}

func mustAddrs(t *testing.T, texts ...string) []netip.Addr {
	t.Helper()
	addrs := make([]netip.Addr, 0, len(texts))
	for _, text := range texts {
		addrs = append(addrs, netip.MustParseAddr(text))
	}
	return addrs
}

func newTestChecker(resolver HostResolver) *URLChecker {
	checker := NewURLChecker(resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checker.timeout = 50 * time.Millisecond
	return checker
}

func TestURLCheckerCheckDestinationSecrets(t *testing.T) {
	resolver := &fakeResolver{
		answers: map[string][]netip.Addr{
			"hooks.example.com":      mustAddrs(t, "93.184.216.34"),
			"dual.example.com":       mustAddrs(t, "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"),
			"mixed.example.com":      mustAddrs(t, "93.184.216.34", "10.0.0.5"),
			"mixed6.example.com":     mustAddrs(t, "2606:2800:220:1:248:1893:25c8:1946", "fd00::1"),
			"privfirst.example.com":  mustAddrs(t, "10.0.0.5", "93.184.216.34"),
			"privfirst6.example.com": mustAddrs(t, "fd00::1", "2606:2800:220:1:248:1893:25c8:1946"),
			"privmiddle.example.com": mustAddrs(t, "93.184.216.34", "10.0.0.5", "2606:2800:220:1:248:1893:25c8:1946"),
			"localhost":              mustAddrs(t, "127.0.0.1"),
			"loop.example.com":       mustAddrs(t, "127.0.0.1"),
			"loop6.example.com":      mustAddrs(t, "::1"),
			"mapped.example.com":     mustAddrs(t, "::ffff:127.0.0.1"),
			"ula.example.com":        mustAddrs(t, "fd12:3456:789a::1"),
			"cgnat.example.com":      mustAddrs(t, "100.64.0.9"),
			"linklocal.example":      mustAddrs(t, "169.254.169.254"),
			"empty.example.com":      {},
			"private.example.com":    mustAddrs(t, "192.168.1.10"),
			"discord.com":            mustAddrs(t, "162.159.135.232"),
			"evil.discord.com":       mustAddrs(t, "10.1.2.3"),
			"hooks.example.com.":     mustAddrs(t, "93.184.216.34"),
		},
		failures: map[string]error{
			"servfail.example.com": errors.New("lookup: server misbehaving"),
			"partial.example.com":  errors.New("lookup: partial answer"),
		},
		partial: map[string][]netip.Addr{
			"partial.example.com": mustAddrs(t, "93.184.216.34"),
		},
		blocking: map[string]bool{"slow.example.com": true},
	}
	const (
		publicURL  = "https://hooks.example.com/hook"
		discordURL = "https://discord.com/api/webhooks/123456/abc_DEF-ghi"
	)
	tests := []struct {
		name            string
		destinationType string
		secrets         map[string]string
		want            string
		wantLookups     int
	}{
		{"webhook public only", TypeWebhook, map[string]string{"url": publicURL}, "", 1},
		{"webhook public ipv4 and ipv6", TypeWebhook, map[string]string{"url": "https://dual.example.com/h"}, "", 1},
		{"webhook mixed public and private v4", TypeWebhook,
			map[string]string{"url": "https://mixed.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook mixed public and ULA", TypeWebhook,
			map[string]string{"url": "https://mixed6.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook private first v4", TypeWebhook,
			map[string]string{"url": "https://privfirst.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook private first v6", TypeWebhook,
			map[string]string{"url": "https://privfirst6.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook private in the middle", TypeWebhook,
			map[string]string{"url": "https://privmiddle.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook localhost resolving to loopback", TypeWebhook,
			map[string]string{"url": "https://localhost/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook decimal integer host", TypeWebhook,
			map[string]string{"url": "https://2130706433/h"}, "secrets.url host could not be resolved", 1},
		{"webhook hex dotted host", TypeWebhook,
			map[string]string{"url": "https://0x7f.0.0.1/h"}, "secrets.url host could not be resolved", 1},
		{"webhook zoned link-local literal", TypeWebhook,
			map[string]string{"url": "https://[fe80::1%25eth0]/h"}, "secrets.url must resolve to public addresses", 0},
		{"webhook addresses returned with an error", TypeWebhook,
			map[string]string{"url": "https://partial.example.com/h"}, "secrets.url host could not be resolved", 1},
		{"webhook trailing dot host", TypeWebhook, map[string]string{"url": "https://hooks.example.com./h"}, "", 1},
		{"webhook loopback v4", TypeWebhook,
			map[string]string{"url": "https://loop.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook loopback v6", TypeWebhook,
			map[string]string{"url": "https://loop6.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook v4-mapped loopback", TypeWebhook,
			map[string]string{"url": "https://mapped.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook IPv6 ULA", TypeWebhook,
			map[string]string{"url": "https://ula.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook CGNAT", TypeWebhook,
			map[string]string{"url": "https://cgnat.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook link-local metadata", TypeWebhook,
			map[string]string{"url": "https://linklocal.example/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook RFC1918", TypeWebhook,
			map[string]string{"url": "https://private.example.com/h"}, "secrets.url must resolve to public addresses", 1},
		{"webhook NXDOMAIN", TypeWebhook,
			map[string]string{"url": "https://nope.example.com/h"}, "secrets.url host could not be resolved", 1},
		{"webhook resolver failure", TypeWebhook,
			map[string]string{"url": "https://servfail.example.com/h"}, "secrets.url host could not be resolved", 1},
		{"webhook empty answer", TypeWebhook,
			map[string]string{"url": "https://empty.example.com/h"}, "secrets.url host could not be resolved", 1},
		{"webhook lookup timeout", TypeWebhook,
			map[string]string{"url": "https://slow.example.com/h"}, "secrets.url host could not be resolved", 1},
		{"webhook public IPv4 literal", TypeWebhook, map[string]string{"url": "https://93.184.216.34/h"}, "", 0},
		{"webhook public IPv6 literal", TypeWebhook,
			map[string]string{"url": "https://[2606:2800:220:1:248:1893:25c8:1946]/h"}, "", 0},
		{"webhook loopback literal", TypeWebhook,
			map[string]string{"url": "https://127.0.0.1/h"}, "secrets.url must resolve to public addresses", 0},
		{"webhook private literal with port", TypeWebhook,
			map[string]string{"url": "https://10.0.0.5:8443/h"}, "secrets.url must resolve to public addresses", 0},
		{"webhook IPv6 loopback literal", TypeWebhook,
			map[string]string{"url": "https://[::1]/h"}, "secrets.url must resolve to public addresses", 0},
		{"webhook v4-mapped loopback literal", TypeWebhook,
			map[string]string{"url": "https://[::ffff:127.0.0.1]/h"}, "secrets.url must resolve to public addresses", 0},
		{"webhook link-local literal", TypeWebhook,
			map[string]string{"url": "https://169.254.169.254/latest/meta-data"}, "secrets.url must resolve to public addresses", 0},
		{"webhook missing url", TypeWebhook, map[string]string{}, "invalid secrets.url", 0},
		{"webhook http scheme", TypeWebhook, map[string]string{"url": "http://hooks.example.com/h"}, "invalid secrets.url", 0},
		{"webhook userinfo", TypeWebhook,
			map[string]string{"url": "https://user:pass@hooks.example.com/h"}, "invalid secrets.url", 0},
		{"discord public", TypeDiscord, map[string]string{"webhook_url": discordURL}, "", 1},
		{"discord resolves private", TypeDiscord,
			map[string]string{"webhook_url": "https://evil.discord.com/api/webhooks/1/abc"},
			"secrets.webhook_url must resolve to public addresses", 1},
		{"discord NXDOMAIN", TypeDiscord,
			map[string]string{"webhook_url": "https://ptb.discord.com/api/webhooks/1/abc"},
			"secrets.webhook_url host could not be resolved", 1},
		{"discord missing url", TypeDiscord, map[string]string{}, "invalid secrets.webhook_url", 0},
		{"ntfy has no url", TypeNtfy, map[string]string{"access_token": "tk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "", 0},
		{"unknown type fails closed", "carrier_pigeon", map[string]string{"url": publicURL}, "invalid type", 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			testResolver := &fakeResolver{
				answers:  resolver.answers,
				failures: resolver.failures,
				partial:  resolver.partial,
				blocking: resolver.blocking,
			}
			checker := newTestChecker(testResolver)
			err := checker.CheckDestinationSecrets(context.Background(), testCase.destinationType, testCase.secrets)
			assertValidationMessage(t, err, testCase.want)
			if len(testResolver.lookups) != testCase.wantLookups {
				t.Fatalf("lookups = %v, want %d", testResolver.lookups, testCase.wantLookups)
			}
		})
	}
}

func TestURLCheckerLooksUpHostnameOnly(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"hooks.example.com": mustAddrs(t, "93.184.216.34")}}
	checker := newTestChecker(resolver)
	err := checker.CheckDestinationSecrets(context.Background(), TypeWebhook,
		map[string]string{"url": "https://hooks.example.com:8443/path?x=1"})
	assertValidationMessage(t, err, "")
	if len(resolver.lookups) != 1 || resolver.lookups[0] != "hooks.example.com" {
		t.Fatalf("lookups = %v, want [hooks.example.com]", resolver.lookups)
	}
}

func TestURLCheckerBoundsLookupWithDefaultTimeout(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"hooks.example.com": mustAddrs(t, "93.184.216.34")}}
	checker := NewURLChecker(resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := checker.CheckDestinationSecrets(context.Background(), TypeWebhook,
		map[string]string{"url": "https://hooks.example.com/h"})
	assertValidationMessage(t, err, "")
	if len(resolver.deadlines) != 1 || resolver.deadlines[0] <= 0 || resolver.deadlines[0] > resolveTimeout {
		t.Fatalf("lookup deadlines = %v, want one deadline within %s", resolver.deadlines, resolveTimeout)
	}
}

func TestURLCheckerHonoursCancelledContext(t *testing.T) {
	resolver := &fakeResolver{blocking: map[string]bool{"slow.example.com": true}}
	checker := NewURLChecker(resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := checker.CheckDestinationSecrets(ctx, TypeWebhook, map[string]string{"url": "https://slow.example.com/h"})
	elapsed := time.Since(started)
	assertValidationMessage(t, err, "secrets.url host could not be resolved")
	// The blocking fake only returns when the lookup context ends. A checker that ignored the caller's
	// cancelled ctx would wait out its own full timeout instead of returning at once.
	if elapsed >= resolveTimeout/2 {
		t.Fatalf("cancelled context took %s, want well under %s", elapsed, resolveTimeout/2)
	}
}

func TestURLCheckerLooksUpTrailingDotHostVerbatim(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"hooks.example.com.": mustAddrs(t, "93.184.216.34")}}
	checker := newTestChecker(resolver)
	err := checker.CheckDestinationSecrets(context.Background(), TypeWebhook,
		map[string]string{"url": "https://hooks.example.com./h"})
	assertValidationMessage(t, err, "")
	if len(resolver.lookups) != 1 || resolver.lookups[0] != "hooks.example.com." {
		t.Fatalf("lookups = %v, want [hooks.example.com.]", resolver.lookups)
	}
}

func TestURLCheckerRefusesNumericHostResolvingToLoopback(t *testing.T) {
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"2130706433": mustAddrs(t, "127.0.0.1")}}
	checker := newTestChecker(resolver)
	err := checker.CheckDestinationSecrets(context.Background(), TypeWebhook,
		map[string]string{"url": "https://2130706433/h"})
	assertValidationMessage(t, err, "secrets.url must resolve to public addresses")
	if len(resolver.lookups) != 1 {
		t.Fatalf("lookups = %v, want 1", resolver.lookups)
	}
}

func TestURLCheckerLogsHostButNeverTheURLPath(t *testing.T) {
	const secretToken = "SECRETTOKEN"
	tests := []struct {
		name       string
		webhookURL string
	}{
		{"resolves to a private address", "https://evil.discord.com/api/webhooks/1/" + secretToken},
		{"does not resolve", "https://ptb.discord.com/api/webhooks/1/" + secretToken},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			resolver := &fakeResolver{answers: map[string][]netip.Addr{"evil.discord.com": mustAddrs(t, "10.1.2.3")}}
			// The checker runs on this goroutine only, so the buffer needs no lock.
			var logBuffer bytes.Buffer
			checker := NewURLChecker(resolver, slog.New(slog.NewTextHandler(&logBuffer, nil)))
			checker.timeout = 50 * time.Millisecond
			err := checker.CheckDestinationSecrets(context.Background(), TypeDiscord,
				map[string]string{"webhook_url": testCase.webhookURL})
			if err == nil {
				t.Fatal("want a validation error, got nil")
			}
			logged := logBuffer.String()
			host, _, _ := strings.Cut(strings.TrimPrefix(testCase.webhookURL, "https://"), "/")
			if !strings.Contains(logged, host) {
				t.Fatalf("log %q does not name host %q", logged, host)
			}
			if strings.Contains(logged, secretToken) {
				t.Fatalf("log leaks the URL token: %q", logged)
			}
		})
	}
}

func TestNewURLCheckerDefaults(t *testing.T) {
	checker := NewURLChecker(nil, nil)
	if checker.resolver == nil || checker.logger == nil || checker.timeout != resolveTimeout {
		t.Fatalf("defaults not applied: %+v", checker)
	}
}
