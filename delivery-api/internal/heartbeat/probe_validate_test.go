package heartbeat

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

func validProbeConfigForTest() ProbeConfig {
	return ProbeConfig{
		URL:              "https://example.com/health",
		ExpectedStatuses: []int{200},
		TimeoutSeconds:   5,
		IntervalSeconds:  300,
	}
}

func TestValidateProbeConfigURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"https ok", "https://example.com", ""},
		{"query allowed", "https://example.com/p?x=1", ""},
		{"port ok", "https://example.com:8443/", ""},
		{"http", "http://example.com", "invalid probe.url"},
		{"upper-case scheme", "HTTPS://example.com", "invalid probe.url"},
		{"mixed-case scheme", "Https://example.com", "invalid probe.url"},
		{"userinfo", "https://user:pw@example.com", "invalid probe.url"},
		{"fragment", "https://example.com/#frag", "invalid probe.url"},
		{"empty fragment marker", "https://example.com/#", "invalid probe.url"},
		{"ipv4", "https://127.0.0.1/", "invalid probe.url"},
		{"ipv6", "https://[::1]/", "invalid probe.url"},
		{"ipv6 zone", "https://[fe80::1%25eth0]/", "invalid probe.url"},
		{"localhost", "https://localhost/", "invalid probe.url"},
		{"LOCALHOST", "https://LOCALHOST/", "invalid probe.url"},
		{"subdomain localhost", "https://a.localhost/", "invalid probe.url"},
		{"trailing dot localhost", "https://localhost./", "invalid probe.url"},
		{"no host", "https:///path", "invalid probe.url"},
		{"unparseable", "https://exa mple.com", "invalid probe.url"},
		{"empty", "", "invalid probe.url"},
		{"exactly max bytes", "https://example.com/" + strings.Repeat("a", MaxProbeURLBytes-len("https://example.com/")), ""},
		{"over max bytes", "https://example.com/" + strings.Repeat("a", MaxProbeURLBytes-len("https://example.com/")+1), "invalid probe.url"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := validProbeConfigForTest()
			config.URL = testCase.url
			assertValidationMessage(t, ValidateProbeConfig(config), testCase.want)
		})
	}
}

func TestValidateProbeConfigBounds(t *testing.T) {
	bodyOf := func(value string) *string { return &value }
	cases := []struct {
		name   string
		mutate func(*ProbeConfig)
		want   string
	}{
		{"valid", func(*ProbeConfig) {}, ""},
		{"statuses empty", func(config *ProbeConfig) { config.ExpectedStatuses = nil }, "invalid probe.expected_statuses"},
		{"statuses 10 ok", func(config *ProbeConfig) {
			config.ExpectedStatuses = []int{200, 201, 202, 203, 204, 205, 206, 207, 208, 226}
		}, ""},
		{"statuses 11", func(config *ProbeConfig) {
			config.ExpectedStatuses = []int{200, 201, 202, 203, 204, 205, 206, 207, 208, 226, 300}
		}, "invalid probe.expected_statuses"},
		{"statuses duplicate", func(config *ProbeConfig) { config.ExpectedStatuses = []int{200, 200} }, "invalid probe.expected_statuses"},
		{"statuses 99", func(config *ProbeConfig) { config.ExpectedStatuses = []int{99} }, "invalid probe.expected_statuses"},
		{"statuses 100", func(config *ProbeConfig) { config.ExpectedStatuses = []int{100} }, ""},
		{"statuses 599", func(config *ProbeConfig) { config.ExpectedStatuses = []int{599} }, ""},
		{"statuses 600", func(config *ProbeConfig) { config.ExpectedStatuses = []int{600} }, "invalid probe.expected_statuses"},
		{"body nil ok", func(config *ProbeConfig) { config.BodyMatch = nil }, ""},
		{"body empty", func(config *ProbeConfig) { config.BodyMatch = bodyOf("") }, "invalid probe.body_match"},
		{"body 256", func(config *ProbeConfig) { config.BodyMatch = bodyOf(strings.Repeat("a", 256)) }, ""},
		{"body 257", func(config *ProbeConfig) { config.BodyMatch = bodyOf(strings.Repeat("a", 257)) }, "invalid probe.body_match"},
		{"body invalid utf8", func(config *ProbeConfig) { config.BodyMatch = bodyOf("a\xffb") }, "invalid probe.body_match"},
		{"body NUL", func(config *ProbeConfig) { config.BodyMatch = bodyOf("a\x00b") }, "invalid probe.body_match"},
		{"interval 59", func(config *ProbeConfig) { config.IntervalSeconds = 59 }, "invalid probe.interval_seconds"},
		{"interval 60", func(config *ProbeConfig) { config.IntervalSeconds = 60; config.TimeoutSeconds = 8 }, ""},
		{"interval 86400", func(config *ProbeConfig) { config.IntervalSeconds = 86400 }, ""},
		{"interval 86401", func(config *ProbeConfig) { config.IntervalSeconds = 86401 }, "invalid probe.interval_seconds"},
		{"timeout 0", func(config *ProbeConfig) { config.TimeoutSeconds = 0 }, "invalid probe.timeout_seconds"},
		{"timeout 1", func(config *ProbeConfig) { config.TimeoutSeconds = 1 }, ""},
		{"timeout 8", func(config *ProbeConfig) { config.TimeoutSeconds = 8 }, ""},
		{"timeout 9", func(config *ProbeConfig) { config.TimeoutSeconds = 9 }, "invalid probe.timeout_seconds"},
		{"timeout equals interval", func(config *ProbeConfig) { config.IntervalSeconds = 60; config.TimeoutSeconds = 60 }, "invalid probe.timeout_seconds"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := validProbeConfigForTest()
			testCase.mutate(&config)
			assertValidationMessage(t, ValidateProbeConfig(config), testCase.want)
		})
	}
}

func TestValidateAccessToken(t *testing.T) {
	cases := []struct {
		name  string
		token AccessToken
		want  string
	}{
		{"valid", AccessToken{ClientID: "abc.access", ClientSecret: "s3cret~!"}, ""},
		{"256 bytes", AccessToken{ClientID: strings.Repeat("a", 256), ClientSecret: strings.Repeat("b", 256)}, ""},
		{"257 id", AccessToken{ClientID: strings.Repeat("a", 257), ClientSecret: "x"}, "invalid probe.access_token"},
		{"257 secret", AccessToken{ClientID: "x", ClientSecret: strings.Repeat("a", 257)}, "invalid probe.access_token"},
		{"empty id", AccessToken{ClientSecret: "x"}, "invalid probe.access_token"},
		{"empty secret", AccessToken{ClientID: "x"}, "invalid probe.access_token"},
		{"both empty", AccessToken{}, "invalid probe.access_token"},
		{"CR", AccessToken{ClientID: "a\rb", ClientSecret: "x"}, "invalid probe.access_token"},
		{"LF", AccessToken{ClientID: "x", ClientSecret: "a\nb"}, "invalid probe.access_token"},
		{"NUL", AccessToken{ClientID: "a\x00", ClientSecret: "x"}, "invalid probe.access_token"},
		{"space", AccessToken{ClientID: "a b", ClientSecret: "x"}, "invalid probe.access_token"},
		{"DEL", AccessToken{ClientID: "a\x7f", ClientSecret: "x"}, "invalid probe.access_token"},
		{"non-ascii", AccessToken{ClientID: "café", ClientSecret: "x"}, "invalid probe.access_token"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateAccessToken(testCase.token)
			assertValidationMessage(t, err, testCase.want)
			if err != nil && (strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "abc")) {
				t.Fatalf("error echoes a value: %v", err)
			}
		})
	}
}

func TestDerivedScheduleStaysInsideCheckRanges(t *testing.T) {
	for _, interval := range []int{MinProbeIntervalSeconds, 300, MaxProbeIntervalSeconds} {
		period, grace := DerivedSchedule(interval)
		if period != interval || grace != interval*(ProbeFailureThreshold+1) {
			t.Fatalf("DerivedSchedule(%d) = %d/%d", interval, period, grace)
		}
		if err := ValidatePeriodSeconds(period); err != nil {
			t.Fatalf("derived period %d out of range: %v", period, err)
		}
		if err := ValidateGraceSeconds(grace); err != nil {
			t.Fatalf("derived grace %d out of range: %v", grace, err)
		}
	}
}

func TestValidateNewCheckProbe(t *testing.T) {
	config := validProbeConfigForTest()
	token := AccessToken{ClientID: "id", ClientSecret: "secret"}
	appOwner := "some_app"
	base := func() NewCheck {
		return NewCheck{AppID: "myapp", Name: "site", DestinationNames: []string{"ops"}, Probe: &config}
	}
	cases := []struct {
		name   string
		mutate func(*NewCheck)
		want   string
	}{
		{"valid probe", func(*NewCheck) {}, ""},
		{"valid probe with token", func(check *NewCheck) { check.AccessToken = &token }, ""},
		{"explicit operator owner", func(check *NewCheck) { check.OwnerKind = registry.OwnerOperator }, ""},
		{"period supplied", func(check *NewCheck) { check.PeriodSeconds = 300 }, "period_seconds and grace_seconds are derived for probe checks"},
		{"grace supplied", func(check *NewCheck) { check.GraceSeconds = 1200 }, "period_seconds and grace_seconds are derived for probe checks"},
		{"schedule key supplied with zero values", func(check *NewCheck) { check.ScheduleSupplied = true }, MessageDerivedSchedule},
		{"schedule supplied before bad config", func(check *NewCheck) {
			check.ScheduleSupplied = true
			bad := validProbeConfigForTest()
			bad.URL = "http://x.example"
			check.Probe = &bad
		}, MessageDerivedSchedule},
		{"bad app_id before schedule supplied", func(check *NewCheck) { check.AppID, check.ScheduleSupplied = "Bad", true }, "invalid app_id"},
		{"bad name before schedule supplied", func(check *NewCheck) { check.Name, check.ScheduleSupplied = "Bad-Name", true }, "invalid name"},
		{"app owner before schedule supplied", func(check *NewCheck) {
			check.OwnerKind, check.OwnerAppID, check.ScheduleSupplied = registry.OwnerApp, &appOwner, true
		}, MessageProbeOnAppOwned},
		{"push ignores ScheduleSupplied", func(check *NewCheck) {
			check.Probe = nil
			check.PeriodSeconds, check.GraceSeconds = 60, 60
			check.ScheduleSupplied = true
		}, ""},
		{"bad config", func(check *NewCheck) {
			bad := validProbeConfigForTest()
			bad.URL = "http://x.example"
			check.Probe = &bad
		}, "invalid probe.url"},
		{"bad token", func(check *NewCheck) { check.AccessToken = &AccessToken{ClientID: "id"} }, "invalid probe.access_token"},
		{"bad destinations", func(check *NewCheck) { check.DestinationNames = nil }, "invalid destinations"},
		{"app owner with probe", func(check *NewCheck) { check.OwnerKind = registry.OwnerApp; check.OwnerAppID = &appOwner }, "probe config is not allowed on an app-owned check"},
		{"unknown owner kind", func(check *NewCheck) { check.OwnerKind = "mystery" }, "probe config is not allowed on an app-owned check"},
		{"app owner with token only", func(check *NewCheck) {
			check.Probe = nil
			check.AccessToken = &token
			check.OwnerKind = registry.OwnerApp
		}, "probe config is not allowed on an app-owned check"},
		{"push with token", func(check *NewCheck) {
			check.Probe = nil
			check.PeriodSeconds, check.GraceSeconds = 60, 60
			check.AccessToken = &token
		}, "invalid probe.access_token"},
		{"push ok", func(check *NewCheck) {
			check.Probe = nil
			check.PeriodSeconds, check.GraceSeconds = 60, 60
		}, ""},
		{"push app owner ok", func(check *NewCheck) {
			check.Probe = nil
			check.PeriodSeconds, check.GraceSeconds = 60, 60
			check.OwnerKind = registry.OwnerApp
			check.OwnerAppID = &appOwner
		}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			check := base()
			testCase.mutate(&check)
			assertValidationMessage(t, ValidateNewCheck(check), testCase.want)
		})
	}
}

func TestValidateCheckUpdateProbe(t *testing.T) {
	config := validProbeConfigForTest()
	period, grace := DerivedSchedule(config.IntervalSeconds)
	update := CheckUpdate{PeriodSeconds: period, GraceSeconds: grace, DestinationNames: []string{"ops"}, Probe: &config}
	assertValidationMessage(t, ValidateCheckUpdate(update), "")

	bad := validProbeConfigForTest()
	bad.TimeoutSeconds = 0
	update.Probe = &bad
	assertValidationMessage(t, ValidateCheckUpdate(update), "invalid probe.timeout_seconds")

	update.Probe = &config
	update.AccessToken = AccessTokenPatch{Action: AccessTokenReplace, Token: AccessToken{ClientID: "id"}}
	assertValidationMessage(t, ValidateCheckUpdate(update), "invalid probe.access_token")

	update.AccessToken = AccessTokenPatch{Action: AccessTokenReplace, Token: AccessToken{ClientID: "id", ClientSecret: "s"}}
	assertValidationMessage(t, ValidateCheckUpdate(update), "")

	update.AccessToken = AccessTokenPatch{Action: AccessTokenClear}
	assertValidationMessage(t, ValidateCheckUpdate(update), "")

	update.AccessToken = AccessTokenPatch{Action: AccessTokenAction(99)}
	assertValidationMessage(t, ValidateCheckUpdate(update), "invalid probe.access_token")
}

func TestValidateCheckPatchForKind(t *testing.T) {
	period := 120
	push := StoredCheck{}
	probe := probeCheckForTest()
	cases := []struct {
		name    string
		current StoredCheck
		patch   CheckPatch
		want    string
	}{
		{"probe patch on push", push, CheckPatch{Probe: &ProbePatch{}}, "probe config is not allowed on a push check"},
		{"token replace on push", push, CheckPatch{AccessToken: AccessTokenPatch{Action: AccessTokenReplace}}, "probe config is not allowed on a push check"},
		{"token clear on push", push, CheckPatch{AccessToken: AccessTokenPatch{Action: AccessTokenClear}}, "probe config is not allowed on a push check"},
		{"destination patch on push", push, CheckPatch{DestinationNames: []string{"ops"}}, ""},
		{"period on push", push, CheckPatch{PeriodSeconds: &period}, ""},
		{"period on probe", probe, CheckPatch{PeriodSeconds: &period}, "period_seconds and grace_seconds are derived for probe checks"},
		{"grace on probe", probe, CheckPatch{GraceSeconds: &period}, "period_seconds and grace_seconds are derived for probe checks"},
		{"destination patch on probe", probe, CheckPatch{DestinationNames: []string{"ops"}}, ""},
		{"probe patch on probe", probe, CheckPatch{Probe: &ProbePatch{}}, ""},
		{"token on probe", probe, CheckPatch{AccessToken: AccessTokenPatch{Action: AccessTokenClear}}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateCheckPatchForKind(testCase.current, testCase.patch), testCase.want)
		})
	}
}

func TestSanitizeProbeDetail(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		want   string
	}{
		{
			"https url with userinfo path query fragment",
			`Get "https://user:pw@host.example/p?token=SECRET#frag": context deadline exceeded`,
			`Get "https://host.example": context deadline exceeded`,
		},
		{"plain http url", "dial http://host.example:8080/a/b?q=1 failed", "dial http://host.example:8080 failed"},
		{"upper-case scheme url", "see HTTPS://Host.Example/x", "see HTTPS://Host.Example"},
		{"no url unchanged", "connection refused", "connection refused"},
		{"control chars", "a\x00b\nc\td", "a b c d"},
		{"empty", "", ""},
		{"blank collapses to empty", "  \n\t ", ""},
		{"surrounding whitespace trimmed", " boom \n", "boom"},
		{"two urls", "a https://one.example/x?q=1 b http://two.example:81/y#f", "a https://one.example b http://two.example:81"},
		{"bare scheme", "http://", "http://"},
		{"ipv6 literal with port", "dial https://[::1]:8080/x failed", "dial https://[::1]:8080 failed"},
		{"url before closing quote", `Get 'https://host.example/p?x=1' failed`, `Get 'https://host.example' failed`},
		{"url before closing angle bracket", "see <https://host.example/p> now", "see <https://host.example> now"},
		{"invalid utf8 replaced", "a\xffb", "a�b"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := SanitizeProbeDetail(testCase.detail)
			if got != testCase.want {
				t.Fatalf("SanitizeProbeDetail = %q, want %q", got, testCase.want)
			}
			for _, secret := range []string{"SECRET", "user:pw", "frag"} {
				if strings.Contains(got, secret) {
					t.Fatalf("result %q still contains %q", got, secret)
				}
			}
		})
	}
}

func TestSanitizeProbeDetailTruncatesOnRuneBoundary(t *testing.T) {
	ascii := strings.Repeat("a", MaxProbeDetailBytes+10)
	if got := SanitizeProbeDetail(ascii); len(got) != MaxProbeDetailBytes {
		t.Fatalf("ascii length = %d, want %d", len(got), MaxProbeDetailBytes)
	}
	// 255 ASCII bytes then a 3-byte rune straddling the 256-byte cut.
	straddle := strings.Repeat("a", MaxProbeDetailBytes-1) + "€€"
	got := SanitizeProbeDetail(straddle)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated detail is not valid UTF-8: %q", got)
	}
	if len(got) != MaxProbeDetailBytes-1 {
		t.Fatalf("length = %d, want %d (rune dropped whole)", len(got), MaxProbeDetailBytes-1)
	}
	exact := strings.Repeat("a", MaxProbeDetailBytes-3) + "€"
	if got := SanitizeProbeDetail(exact); got != exact {
		t.Fatalf("a detail of exactly %d bytes must be unchanged", MaxProbeDetailBytes)
	}
}
