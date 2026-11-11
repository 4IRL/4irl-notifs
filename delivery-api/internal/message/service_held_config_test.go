package message

import (
	"strings"
	"testing"
)

func assertInvalidEnvError(testInstance *testing.T, err error, envVar, value string) {
	testInstance.Helper()
	if err == nil {
		testInstance.Fatalf("%s=%q: want an error", envVar, value)
	}
	if err.Error() != "invalid "+envVar {
		testInstance.Fatalf("error %q, want %q", err, "invalid "+envVar)
	}
	if value != "" && strings.Contains(err.Error(), value) {
		testInstance.Fatalf("error %q must not contain the raw value %q", err, value)
	}
}

func TestHoldUnroutedFromEnv(testInstance *testing.T) {
	testCases := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{name: "empty is on", value: "", want: true},
		{name: "zero is off", value: "0", want: false},
		{name: "false is off", value: "false", want: false},
		{name: "one is on", value: "1", want: true},
		{name: "true is on", value: "true", want: true},
		{name: "yes is an error", value: "yes", wantErr: true},
		{name: "upper case TRUE is an error", value: "TRUE", wantErr: true},
		{name: "padded", value: " 1", wantErr: true},
		{name: "garbage", value: "secret-looking-value", wantErr: true},
		{name: "two", value: "2", wantErr: true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			got, err := HoldUnroutedFromEnv(func(name string) string {
				if name != "DELIVERY_HOLD_UNROUTED" {
					subTest.Fatalf("read env %q, want DELIVERY_HOLD_UNROUTED", name)
				}
				return testCase.value
			})
			if testCase.wantErr {
				assertInvalidEnvError(subTest, err, "DELIVERY_HOLD_UNROUTED", testCase.value)
				if got {
					subTest.Fatal("an invalid value must not turn the hold on")
				}
				return
			}
			if err != nil || got != testCase.want {
				subTest.Fatalf("HoldUnroutedFromEnv(%q) = (%v, %v), want %v", testCase.value, got, err, testCase.want)
			}
		})
	}
}

func TestMaxHeldCapsFromEnv(testInstance *testing.T) {
	parsers := []struct {
		name         string
		envVar       string
		parse        func(func(string) string) (int, error)
		defaultValue int
		limit        string
		overLimit    string
		limitValue   int
	}{
		{
			name: "per app", envVar: "DELIVERY_MAX_HELD_PER_APP", parse: MaxHeldPerAppFromEnv,
			defaultValue: 1000, limit: "100000", overLimit: "100001", limitValue: 100000,
		},
		{
			name: "total", envVar: "DELIVERY_MAX_HELD_TOTAL", parse: MaxHeldTotalFromEnv,
			defaultValue: 100000, limit: "10000000", overLimit: "10000001", limitValue: 10000000,
		},
	}
	for _, parser := range parsers {
		testInstance.Run(parser.name, func(parserTest *testing.T) {
			valid := []struct {
				value string
				want  int
			}{
				{value: "", want: parser.defaultValue},
				{value: "1", want: 1},
				{value: "250", want: 250},
				{value: parser.limit, want: parser.limitValue},
			}
			for _, testCase := range valid {
				got, err := parser.parse(func(name string) string {
					if name != parser.envVar {
						parserTest.Fatalf("read env %q, want %s", name, parser.envVar)
					}
					return testCase.value
				})
				if err != nil || got != testCase.want {
					parserTest.Fatalf("parse(%q) = (%d, %v), want %d", testCase.value, got, err, testCase.want)
				}
			}
			for _, value := range []string{"0", "-3", parser.overLimit, "abc", " 5 ", "1.5", "99999999999999999999"} {
				_, err := parser.parse(func(string) string { return value })
				assertInvalidEnvError(parserTest, err, parser.envVar, value)
			}
		})
	}
}

func TestNewServiceDefaultsHeldSettings(testInstance *testing.T) {
	fixture := newServiceFixture()
	if fixture.service.holdUnrouted {
		testInstance.Fatal("a zero ServiceConfig keeps hold off")
	}
	if got := fixture.service.heldCaps; got.PerApp != DefaultMaxHeldPerApp || got.Total != DefaultMaxHeldTotal {
		testInstance.Fatalf("heldCaps = %+v, want the defaults", got)
	}
}
