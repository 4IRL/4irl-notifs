package registry

import (
	"errors"
	"strings"
	"testing"
)

func assertValidationMessage(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
	}
	if validationErr.Message != want {
		t.Fatalf("message = %q, want %q", validationErr.Message, want)
	}
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestValidateDestinationName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "ops_alerts", ""},
		{"single char", "a", ""},
		{"digit first", "1abc", ""},
		{"63 chars", strings.Repeat("a", 63), ""},
		{"empty", "", "invalid name"},
		{"uppercase", "Ops", "invalid name"},
		{"hyphen", "a-b", "invalid name"},
		{"leading underscore", "_x", "invalid name"},
		{"64 chars", strings.Repeat("a", 64), "invalid name"},
		{"trailing newline", "ops\n", "invalid name"},
		{"space", "ops alerts", "invalid name"},
		{"non ascii", "café", "invalid name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateDestinationName(test.input), test.want)
		})
	}
}

func TestValidateDestinationType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ntfy", ""},
		{"discord", ""},
		{"webhook", ""},
		{"", "invalid type"},
		{"NTFY", "invalid type"},
		{"slack", "invalid type"},
		{"ntfy ", "invalid type"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			assertValidationMessage(t, ValidateDestinationType(test.input), test.want)
		})
	}
}

func TestValidateAppPattern(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"wildcard", "*", ""},
		{"exact", "urls4irl", ""},
		{"63 chars", strings.Repeat("a", 63), ""},
		{"reserved everyone", "everyone", "invalid app_pattern"},
		{"empty", "", "invalid app_pattern"},
		{"prefix wildcard not allowed", "u4i_*", "invalid app_pattern"},
		{"uppercase", "Urls", "invalid app_pattern"},
		{"hyphen", "a-b", "invalid app_pattern"},
		{"64 chars", strings.Repeat("a", 64), "invalid app_pattern"},
		{"double wildcard", "**", "invalid app_pattern"},
		{"trailing newline", "urls4irl\n", "invalid app_pattern"},
		{"non ascii", "café", "invalid app_pattern"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateAppPattern(test.input), test.want)
		})
	}
}

func TestValidateChannelPattern(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"wildcard", "*", ""},
		{"exact", "backup_done", ""},
		{"32 chars exact", strings.Repeat("a", 32), ""},
		{"prefix", "backup_*", ""},
		{"prefix single char", "a*", ""},
		{"prefix max (31 chars + star)", strings.Repeat("a", 31) + "*", ""},
		{"empty", "", "invalid channel_pattern"},
		{"33 chars exact", strings.Repeat("a", 33), "invalid channel_pattern"},
		{"prefix too long", strings.Repeat("a", 32) + "*", "invalid channel_pattern"},
		{"leading star", "*backup", "invalid channel_pattern"},
		{"middle star", "back*up", "invalid channel_pattern"},
		{"double star", "**", "invalid channel_pattern"},
		{"star after underscore start", "_*", "invalid channel_pattern"},
		{"uppercase", "Backup", "invalid channel_pattern"},
		{"hyphen", "back-up", "invalid channel_pattern"},
		{"trailing newline", "backup\n", "invalid channel_pattern"},
		{"prefix trailing newline", "backup_*\n", "invalid channel_pattern"},
		{"non ascii", "café", "invalid channel_pattern"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateChannelPattern(test.input), test.want)
		})
	}
}

func TestValidateRouteDestinations(t *testing.T) {
	twentyOneNames := make([]string, 0, 21)
	for index := 0; index < 21; index++ {
		twentyOneNames = append(twentyOneNames, "dest_"+strings.Repeat("a", index+1))
	}
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{"one", []string{"ops"}, ""},
		{"two", []string{"ops", "discord_main"}, ""},
		{"twenty", twentyOneNames[:20], ""},
		{"nil", nil, "invalid destinations"},
		{"empty", []string{}, "invalid destinations"},
		{"twenty one", twentyOneNames, "invalid destinations"},
		{"invalid name", []string{"ops", "Bad"}, "invalid destinations"},
		{"duplicate", []string{"ops", "ops"}, "invalid destinations"},
		{"empty name", []string{""}, "invalid destinations"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateRouteDestinations(test.input), test.want)
		})
	}
}

const validNtfyToken = "tk_abcdefghijklmnopqrstuvwxyz012"

func TestValidateDestinationFieldsNtfy(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]string
		secrets map[string]string
		want    string
	}{
		{"topic only", map[string]string{"topic": "my-topic_1"}, nil, ""},
		{"topic and token", map[string]string{"topic": "t"}, map[string]string{"access_token": validNtfyToken}, ""},
		{"64 char topic", map[string]string{"topic": strings.Repeat("a", 64)}, nil, ""},
		{"empty secrets map", map[string]string{"topic": "t"}, map[string]string{}, ""},
		{"missing topic", nil, nil, "invalid config.topic"},
		{"empty topic", map[string]string{"topic": ""}, nil, "invalid config.topic"},
		{"topic too long", map[string]string{"topic": strings.Repeat("a", 65)}, nil, "invalid config.topic"},
		{"topic with slash", map[string]string{"topic": "a/b"}, nil, "invalid config.topic"},
		{"topic with newline", map[string]string{"topic": "abc\n"}, nil, "invalid config.topic"},
		{"token bad prefix", map[string]string{"topic": "t"}, map[string]string{"access_token": "xx_abcdefghijklmnopqrstuvwxyz012"}, "invalid secrets.access_token"},
		{"token too short", map[string]string{"topic": "t"}, map[string]string{"access_token": "tk_abc"}, "invalid secrets.access_token"},
		{"token uppercase", map[string]string{"topic": "t"}, map[string]string{"access_token": "tk_ABCDEFGHIJKLMNOPQRSTUVWXYZ012"}, "invalid secrets.access_token"},
		{"token empty", map[string]string{"topic": "t"}, map[string]string{"access_token": ""}, "invalid secrets.access_token"},
		{"token one char too long", map[string]string{"topic": "t"}, map[string]string{"access_token": validNtfyToken + "a"}, "invalid secrets.access_token"},
		{"token one char too short", map[string]string{"topic": "t"}, map[string]string{"access_token": validNtfyToken[:len(validNtfyToken)-1]}, "invalid secrets.access_token"},
		{"token trailing newline", map[string]string{"topic": "t"}, map[string]string{"access_token": validNtfyToken + "\n"}, "invalid secrets.access_token"},
		{"topic non ascii", map[string]string{"topic": "café"}, nil, "invalid config.topic"},
		{"two unknown config keys report the first sorted", map[string]string{"topic": "t", "b": "x", "a": "x"}, nil, "unknown field config.a"},
		{"unknown key with unsafe characters is sanitized", map[string]string{"topic": "t", "bad key\n": "x"}, nil, "unknown field config.bad?key?"},
		{"unknown key is truncated", map[string]string{"topic": "t", strings.Repeat("k", 200): "x"}, nil, "unknown field config." + strings.Repeat("k", 64)},
		{"unknown config key", map[string]string{"topic": "t", "extra": "x"}, nil, "unknown field config.extra"},
		{"unknown secrets key", map[string]string{"topic": "t"}, map[string]string{"foo": "x"}, "unknown field secrets.foo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateDestinationFields(TypeNtfy, test.config, test.secrets), test.want)
		})
	}
}

func TestValidateDestinationFieldsDiscord(t *testing.T) {
	const validURL = "https://discord.com/api/webhooks/123456/abcdef"
	tests := []struct {
		name    string
		config  map[string]string
		secrets map[string]string
		want    string
	}{
		{"discord.com", nil, map[string]string{"webhook_url": validURL}, ""},
		{"discordapp.com", nil, map[string]string{"webhook_url": "https://discordapp.com/api/webhooks/1/a"}, ""},
		{"ptb", nil, map[string]string{"webhook_url": "https://ptb.discord.com/api/webhooks/1/a"}, ""},
		{"canary", nil, map[string]string{"webhook_url": "https://canary.discord.com/api/webhooks/1/a"}, ""},
		{"uppercase host", nil, map[string]string{"webhook_url": "https://DISCORD.com/api/webhooks/1/a"}, ""},
		{"empty config map", map[string]string{}, map[string]string{"webhook_url": validURL}, ""},
		{"missing url", nil, nil, "invalid secrets.webhook_url"},
		{"http scheme", nil, map[string]string{"webhook_url": "http://discord.com/api/webhooks/1/a"}, "invalid secrets.webhook_url"},
		{"wrong host", nil, map[string]string{"webhook_url": "https://example.com/api/webhooks/1/a"}, "invalid secrets.webhook_url"},
		{"lookalike host", nil, map[string]string{"webhook_url": "https://discord.com.evil.test/api/webhooks/1/a"}, "invalid secrets.webhook_url"},
		{"host with port", nil, map[string]string{"webhook_url": "https://discord.com:8443/api/webhooks/1/a"}, "invalid secrets.webhook_url"},
		{"wrong path", nil, map[string]string{"webhook_url": "https://discord.com/other/1/a"}, "invalid secrets.webhook_url"},
		{"prefix only", nil, map[string]string{"webhook_url": "https://discord.com/api/webhooks/"}, "invalid secrets.webhook_url"},
		{"empty token", nil, map[string]string{"webhook_url": "https://discord.com/api/webhooks/123/"}, "invalid secrets.webhook_url"},
		{"non numeric id", nil, map[string]string{"webhook_url": "https://discord.com/api/webhooks/abc/tok"}, "invalid secrets.webhook_url"},
		{"encoded traversal", nil, map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/..%2f..%2foauth2"}, "invalid secrets.webhook_url"},
		{"url too long", nil, map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/" + strings.Repeat("a", 2048)}, "invalid secrets.webhook_url"},
		{"userinfo", nil, map[string]string{"webhook_url": "https://user:pw@discord.com/api/webhooks/1/a"}, "invalid secrets.webhook_url"},
		{"unparseable", nil, map[string]string{"webhook_url": "https://discord.com/%zz"}, "invalid secrets.webhook_url"},
		{"unknown config key", map[string]string{"topic": "t"}, map[string]string{"webhook_url": validURL}, "unknown field config.topic"},
		{"unknown secrets key", nil, map[string]string{"webhook_url": validURL, "url": validURL}, "unknown field secrets.url"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateDestinationFields(TypeDiscord, test.config, test.secrets), test.want)
		})
	}
}

func TestValidateDestinationFieldsWebhook(t *testing.T) {
	signingSecret := strings.Repeat("s", 32)
	tests := []struct {
		name    string
		config  map[string]string
		secrets map[string]string
		want    string
	}{
		{"valid", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": signingSecret}, ""},
		{"max signing secret", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 256)}, ""},
		{"printable punctuation", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": "!~ " + strings.Repeat("x", 29)}, ""},
		{"url with port and query", nil, map[string]string{"url": "https://example.com:8443/hook?a=b", "signing_secret": signingSecret}, ""},
		{"missing url", nil, map[string]string{"signing_secret": signingSecret}, "invalid secrets.url"},
		{"missing signing secret", nil, map[string]string{"url": "https://example.com/hook"}, "invalid secrets.signing_secret"},
		{"http scheme", nil, map[string]string{"url": "http://example.com/hook", "signing_secret": signingSecret}, "invalid secrets.url"},
		{"no host", nil, map[string]string{"url": "https:///hook", "signing_secret": signingSecret}, "invalid secrets.url"},
		{"userinfo", nil, map[string]string{"url": "https://u:p@example.com/hook", "signing_secret": signingSecret}, "invalid secrets.url"},
		{"url too long", nil, map[string]string{"url": "https://example.com/" + strings.Repeat("a", 2048), "signing_secret": signingSecret}, "invalid secrets.url"},
		{"url 2049", nil, map[string]string{"url": "https://example.com/" + strings.Repeat("a", 2049-len("https://example.com/")), "signing_secret": signingSecret}, "invalid secrets.url"},
		{"signing secret DEL", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 31) + "\x7f"}, "invalid secrets.signing_secret"},
		{"signing secret unit separator", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 31) + "\x1f"}, "invalid secrets.signing_secret"},
		{"signing secret tilde", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 31) + "~"}, ""},
		{"url exactly 2048", nil, map[string]string{"url": "https://example.com/" + strings.Repeat("a", 2048-len("https://example.com/")), "signing_secret": signingSecret}, ""},
		{"signing secret too short", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 31)}, "invalid secrets.signing_secret"},
		{"signing secret too long", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 257)}, "invalid secrets.signing_secret"},
		{"signing secret non ascii", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 31) + "é"}, "invalid secrets.signing_secret"},
		{"signing secret control char", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": strings.Repeat("s", 31) + "\n"}, "invalid secrets.signing_secret"},
		{"unknown config key", map[string]string{"topic": "t"}, map[string]string{"url": "https://example.com/hook", "signing_secret": signingSecret}, "unknown field config.topic"},
		{"unknown secrets key", nil, map[string]string{"url": "https://example.com/hook", "signing_secret": signingSecret, "foo": "x"}, "unknown field secrets.foo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateDestinationFields(TypeWebhook, test.config, test.secrets), test.want)
		})
	}
}

func TestValidateDestinationFieldsUnknownType(t *testing.T) {
	tests := []struct {
		name            string
		destinationType string
	}{
		{"unsupported type", "slack"},
		{"empty type", ""},
		{"wrong case", "NTFY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationMessage(t, ValidateDestinationFields(test.destinationType, nil, nil), "invalid type")
		})
	}
}

// Compile-time assertion that *ValidationError satisfies error.
var _ error = (*ValidationError)(nil)
