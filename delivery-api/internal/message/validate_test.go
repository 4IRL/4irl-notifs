package message

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// wantValidationMessage fails the test unless err is a *ValidationError with the
// given message (wantMessage empty means err must be nil).
func wantValidationMessage(testInstance *testing.T, err error, wantMessage string) {
	testInstance.Helper()
	if wantMessage == "" {
		if err != nil {
			testInstance.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil {
		testInstance.Fatalf("error = nil, want ValidationError %q", wantMessage)
	}
	validationError, isValidation := err.(*ValidationError)
	if !isValidation {
		testInstance.Fatalf("error type = %T, want *ValidationError", err)
	}
	if validationError.Message != wantMessage {
		testInstance.Fatalf("message = %q, want %q", validationError.Message, wantMessage)
	}
}

func TestValidateAppID(testInstance *testing.T) {
	testCases := []struct {
		name        string
		appID       string
		wantMessage string
	}{
		{name: "simple slug", appID: "urls4irl"},
		{name: "digit start", appID: "4irl_notifs"},
		{name: "63 characters", appID: strings.Repeat("a", 63)},
		{name: "64 characters", appID: strings.Repeat("a", 64), wantMessage: "invalid app_id"},
		{name: "empty", appID: "", wantMessage: "invalid app_id"},
		{name: "uppercase", appID: "Urls", wantMessage: "invalid app_id"},
		{name: "leading underscore", appID: "_urls", wantMessage: "invalid app_id"},
		{name: "hyphen", appID: "urls-4irl", wantMessage: "invalid app_id"},
		{name: "trailing newline", appID: "urls\n", wantMessage: "invalid app_id"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidateAppID(testCase.appID), testCase.wantMessage)
		})
	}
}

func TestValidateChannel(testInstance *testing.T) {
	testCases := []struct {
		name        string
		channel     string
		wantMessage string
	}{
		{name: "simple slug", channel: "alerts"},
		{name: "32 characters", channel: strings.Repeat("c", 32)},
		{name: "33 characters", channel: strings.Repeat("c", 33), wantMessage: "invalid channel"},
		{name: "empty", channel: "", wantMessage: "invalid channel"},
		{name: "wildcard", channel: "*", wantMessage: "invalid channel"},
		{name: "prefix pattern", channel: "backup_*", wantMessage: "invalid channel"},
		{name: "uppercase", channel: "Alerts", wantMessage: "invalid channel"},
		{name: "hyphen", channel: "a-b", wantMessage: "invalid channel"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidateChannel(testCase.channel), testCase.wantMessage)
		})
	}
}

func TestValidateIdempotencyKey(testInstance *testing.T) {
	testCases := []struct {
		name        string
		key         string
		wantMessage string
	}{
		{name: "simple", key: "backup-2026-10-16"},
		{name: "single character", key: "k"},
		{name: "all printable punctuation", key: "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"},
		{name: "200 bytes", key: strings.Repeat("k", 200)},
		{name: "201 bytes", key: strings.Repeat("k", 201), wantMessage: "invalid idempotency_key"},
		{name: "empty", key: "", wantMessage: "invalid idempotency_key"},
		{name: "space", key: "a b", wantMessage: "invalid idempotency_key"},
		{name: "tab", key: "a\tb", wantMessage: "invalid idempotency_key"},
		{name: "trailing newline", key: "key\n", wantMessage: "invalid idempotency_key"},
		{name: "non-ASCII", key: "clé", wantMessage: "invalid idempotency_key"},
		{name: "delete character", key: "a\x7fb", wantMessage: "invalid idempotency_key"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidateIdempotencyKey(testCase.key), testCase.wantMessage)
		})
	}
}

func TestValidateTo(testInstance *testing.T) {
	testCases := []struct {
		name          string
		toDestination string
		toRecipient   string
		wantMessage   string
	}{
		{name: "neither set"},
		{name: "valid destination", toDestination: "ops_webhook"},
		{name: "63 character destination", toDestination: strings.Repeat("d", 63)},
		{name: "64 character destination", toDestination: strings.Repeat("d", 64), wantMessage: "invalid to.destination"},
		{name: "uppercase destination", toDestination: "Ops", wantMessage: "invalid to.destination"},
		{name: "hyphen destination", toDestination: "ops-webhook", wantMessage: "invalid to.destination"},
		{name: "recipient rejected", toRecipient: "someone", wantMessage: "recipient delivery not yet supported"},
		{
			name: "recipient wins over a bad destination", toDestination: "Bad-Name", toRecipient: "someone",
			wantMessage: "recipient delivery not yet supported",
		},
		{
			name: "recipient rejected alongside a valid destination", toDestination: "ops_webhook", toRecipient: "someone",
			wantMessage: "recipient delivery not yet supported",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidateTo(testCase.toDestination, testCase.toRecipient), testCase.wantMessage)
		})
	}
}

func TestValidateDeliverAt(testInstance *testing.T) {
	someTime := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	zeroTime := time.Time{}
	testCases := []struct {
		name        string
		deliverAt   *time.Time
		wantMessage string
	}{
		{name: "absent"},
		{name: "future time rejected", deliverAt: &someTime, wantMessage: "deliver_at not yet supported"},
		{name: "zero time rejected", deliverAt: &zeroTime, wantMessage: "deliver_at not yet supported"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidateDeliverAt(testCase.deliverAt), testCase.wantMessage)
		})
	}
}

func TestValidatePayload(testInstance *testing.T) {
	// maxPayload is exactly MaxPayloadBytes of valid JSON: {"k":"vvv...v"}.
	const wrapperBytes = len(`{"k":""}`)
	maxPayload := `{"k":"` + strings.Repeat("v", MaxPayloadBytes-wrapperBytes) + `"}`
	overPayload := `{"k":"` + strings.Repeat("v", MaxPayloadBytes-wrapperBytes+1) + `"}`

	testCases := []struct {
		name        string
		payload     json.RawMessage
		wantMessage string
	}{
		{name: "empty object", payload: json.RawMessage(`{}`)},
		{name: "object with fields", payload: json.RawMessage(`{"a":1,"b":[true,null]}`)},
		{name: "leading whitespace", payload: json.RawMessage("  \n{\"a\":1}")},
		{name: "exactly the cap", payload: json.RawMessage(maxPayload)},
		{name: "one byte over the cap", payload: json.RawMessage(overPayload), wantMessage: "payload too large"},
		{name: "nil", payload: nil, wantMessage: "invalid payload"},
		{name: "empty", payload: json.RawMessage(``), wantMessage: "invalid payload"},
		{name: "whitespace only", payload: json.RawMessage("   "), wantMessage: "invalid payload"},
		{name: "array", payload: json.RawMessage(`[]`), wantMessage: "invalid payload"},
		{name: "string", payload: json.RawMessage(`"text"`), wantMessage: "invalid payload"},
		{name: "number", payload: json.RawMessage(`1`), wantMessage: "invalid payload"},
		{name: "null", payload: json.RawMessage(`null`), wantMessage: "invalid payload"},
		{name: "truncated object", payload: json.RawMessage(`{"a":`), wantMessage: "invalid payload"},
		{name: "trailing data", payload: json.RawMessage(`{} {}`), wantMessage: "invalid payload"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidatePayload(testCase.payload), testCase.wantMessage)
		})
	}
}

func TestValidateDisplay(testInstance *testing.T) {
	testCases := []struct {
		name        string
		display     *Display
		wantMessage string
	}{
		{name: "nil", display: nil},
		{name: "empty", display: &Display{}},
		{
			name: "fully populated",
			display: &Display{
				Title: "Backup done", Body: "All good", URL: "https://example.com/run/1", Priority: "high",
			},
		},
		{name: "250 byte title", display: &Display{Title: strings.Repeat("t", 250)}},
		{name: "251 byte title", display: &Display{Title: strings.Repeat("t", 251)}, wantMessage: "invalid display.title"},
		{name: "multibyte title counted in bytes", display: &Display{Title: strings.Repeat("é", 126)}, wantMessage: "invalid display.title"},
		{name: "4096 byte body", display: &Display{Body: strings.Repeat("b", 4096)}},
		{name: "4097 byte body", display: &Display{Body: strings.Repeat("b", 4097)}, wantMessage: "invalid display.body"},
		{name: "http url", display: &Display{URL: "http://example.com"}, wantMessage: "invalid display.url"},
		{name: "javascript url", display: &Display{URL: "javascript:alert(1)"}, wantMessage: "invalid display.url"},
		{name: "relative url", display: &Display{URL: "/run/1"}, wantMessage: "invalid display.url"},
		{name: "url without host", display: &Display{URL: "https:///path"}, wantMessage: "invalid display.url"},
		{name: "url with userinfo", display: &Display{URL: "https://user:pass@example.com"}, wantMessage: "invalid display.url"},
		{name: "unparseable url", display: &Display{URL: "https://exa mple.com"}, wantMessage: "invalid display.url"},
		{
			name:    "2048 byte url",
			display: &Display{URL: "https://example.com/" + strings.Repeat("u", 2048-len("https://example.com/"))},
		},
		{
			name:        "2049 byte url",
			display:     &Display{URL: "https://example.com/" + strings.Repeat("u", 2049-len("https://example.com/"))},
			wantMessage: "invalid display.url",
		},
		{name: "min priority", display: &Display{Priority: "min"}},
		{name: "low priority", display: &Display{Priority: "low"}},
		{name: "default priority", display: &Display{Priority: "default"}},
		{name: "high priority", display: &Display{Priority: "high"}},
		{name: "urgent priority", display: &Display{Priority: "urgent"}},
		{name: "unknown priority", display: &Display{Priority: "critical"}, wantMessage: "invalid display.priority"},
		{name: "uppercase priority", display: &Display{Priority: "High"}, wantMessage: "invalid display.priority"},
		{
			name:        "title reported before body",
			display:     &Display{Title: strings.Repeat("t", 251), Body: strings.Repeat("b", 4097)},
			wantMessage: "invalid display.title",
		},
		{
			name:        "body reported before url",
			display:     &Display{Body: strings.Repeat("b", 4097), URL: "http://example.com"},
			wantMessage: "invalid display.body",
		},
		{
			name:        "url reported before priority",
			display:     &Display{URL: "http://example.com", Priority: "nope"},
			wantMessage: "invalid display.url",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidateDisplay(testCase.display), testCase.wantMessage)
		})
	}
}

// validEnvelope returns an envelope that passes every rule.
func validEnvelope() Envelope {
	return Envelope{
		AppID:          "urls4irl",
		Channel:        "alerts",
		IdempotencyKey: "key-1",
		Payload:        json.RawMessage(`{"a":1}`),
	}
}

func TestValidateEnvelopeAcceptsValid(testInstance *testing.T) {
	envelope := validEnvelope()
	envelope.ToDestination = "ops_webhook"
	envelope.Critical = true
	envelope.Display = &Display{Title: "t", URL: "https://example.com", Priority: "urgent"}
	wantValidationMessage(testInstance, ValidateEnvelope(envelope), "")
}

func TestValidateEnvelopeEachRule(testInstance *testing.T) {
	someTime := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	testCases := []struct {
		name        string
		mutate      func(envelope *Envelope)
		wantMessage string
	}{
		{name: "bad app_id", mutate: func(envelope *Envelope) { envelope.AppID = "Bad" }, wantMessage: "invalid app_id"},
		{name: "bad channel", mutate: func(envelope *Envelope) { envelope.Channel = "a-b" }, wantMessage: "invalid channel"},
		{name: "bad idempotency_key", mutate: func(envelope *Envelope) { envelope.IdempotencyKey = "" }, wantMessage: "invalid idempotency_key"},
		{name: "bad to.destination", mutate: func(envelope *Envelope) { envelope.ToDestination = "No-Good" }, wantMessage: "invalid to.destination"},
		{
			name:        "recipient",
			mutate:      func(envelope *Envelope) { envelope.ToRecipient = "someone" },
			wantMessage: "recipient delivery not yet supported",
		},
		{name: "deliver_at", mutate: func(envelope *Envelope) { envelope.DeliverAt = &someTime }, wantMessage: "deliver_at not yet supported"},
		{name: "bad payload", mutate: func(envelope *Envelope) { envelope.Payload = json.RawMessage(`[]`) }, wantMessage: "invalid payload"},
		{
			name:        "bad display",
			mutate:      func(envelope *Envelope) { envelope.Display = &Display{Priority: "nope"} },
			wantMessage: "invalid display.priority",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			envelope := validEnvelope()
			testCase.mutate(&envelope)
			wantValidationMessage(subTest, ValidateEnvelope(envelope), testCase.wantMessage)
		})
	}
}

// TestValidateEnvelopeEarlierRuleWins makes each rule's failure coexist with
// the failure of the next rule and checks the earlier one is reported.
func TestValidateEnvelopeEarlierRuleWins(testInstance *testing.T) {
	someTime := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	testCases := []struct {
		name        string
		envelope    Envelope
		wantMessage string
	}{
		{
			name:        "app_id before channel",
			envelope:    Envelope{AppID: "Bad", Channel: "a-b", IdempotencyKey: "", Payload: json.RawMessage(`[]`)},
			wantMessage: "invalid app_id",
		},
		{
			name:        "channel before idempotency_key",
			envelope:    Envelope{AppID: "urls4irl", Channel: "a-b", IdempotencyKey: "", Payload: json.RawMessage(`[]`)},
			wantMessage: "invalid channel",
		},
		{
			name:        "idempotency_key before to",
			envelope:    Envelope{AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "", ToRecipient: "someone", Payload: json.RawMessage(`[]`)},
			wantMessage: "invalid idempotency_key",
		},
		{
			name: "to before deliver_at",
			envelope: Envelope{
				AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1", ToRecipient: "someone",
				DeliverAt: &someTime, Payload: json.RawMessage(`[]`),
			},
			wantMessage: "recipient delivery not yet supported",
		},
		{
			name: "deliver_at before payload",
			envelope: Envelope{
				AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1",
				DeliverAt: &someTime, Payload: json.RawMessage(`[]`),
			},
			wantMessage: "deliver_at not yet supported",
		},
		{
			name: "payload before display",
			envelope: Envelope{
				AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1",
				Payload: json.RawMessage(`[]`), Display: &Display{Priority: "nope"},
			},
			wantMessage: "invalid payload",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			wantValidationMessage(subTest, ValidateEnvelope(testCase.envelope), testCase.wantMessage)
		})
	}
}
