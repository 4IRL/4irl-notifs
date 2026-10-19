package message

import (
	"encoding/json"
	"testing"
)

func TestSamePayloadComparesNumbersByExactValue(testInstance *testing.T) {
	testCases := []struct {
		name      string
		stored    string
		submitted string
		want      bool
	}{
		{name: "identical", stored: `{"n":1}`, submitted: `{"n":1}`, want: true},
		{name: "key order and whitespace", stored: `{"a": 1, "b": [1, 2]}`, submitted: ` {"b":[1,2],"a":1} `, want: true},
		{name: "exponent against integer", stored: `{"n":100}`, submitted: `{"n":1e2}`, want: true},
		{name: "upper case exponent against integer", stored: `{"n":100}`, submitted: `{"n":1E2}`, want: true},
		{name: "positive exponent sign", stored: `{"n":100}`, submitted: `{"n":1e+2}`, want: true},
		{name: "negative zero against zero", stored: `{"n":0}`, submitted: `{"n":-0}`, want: true},
		{name: "negative exponent against decimal", stored: `{"n":0.0000001}`, submitted: `{"n":1e-7}`, want: true},
		{name: "upper case negative exponent", stored: `{"n":0.01}`, submitted: `{"n":1E-2}`, want: true},
		{name: "trailing zero against integer", stored: `{"n":1}`, submitted: `{"n":1.0}`, want: true},
		{name: "trailing zeros against decimal", stored: `{"n":1.5}`, submitted: `{"n":1.500}`, want: true},
		{name: "decimal against exponent", stored: `{"n":1500}`, submitted: `{"n":1.5e3}`, want: true},
		{name: "nested in arrays and objects", stored: `{"a":[{"n":100}]}`, submitted: `{"a":[{"n":1e2}]}`, want: true},
		{name: "same big integer", stored: `{"id":9007199254740993}`, submitted: `{"id":9007199254740993}`, want: true},
		{
			name:      "big integer against its exponent form",
			stored:    `{"id":12345678901234567890123}`,
			submitted: `{"id":1.2345678901234567890123e22}`,
			want:      true,
		},
		{name: "adjacent integers beyond float64", stored: `{"id":9007199254740992}`, submitted: `{"id":9007199254740993}`},
		{
			name:      "distinct big integers sharing a float64",
			stored:    `{"id":12345678901234567890123}`,
			submitted: `{"id":12345678901234567890124}`,
		},
		{
			name:      "distinct big decimals sharing a float64",
			stored:    `{"n":0.1000000000000000000001}`,
			submitted: `{"n":0.1000000000000000000002}`,
		},
		{name: "different numbers", stored: `{"n":1}`, submitted: `{"n":2}`},
		{name: "sign differs", stored: `{"n":1}`, submitted: `{"n":-1}`},
		{name: "number against string", stored: `{"n":1}`, submitted: `{"n":"1"}`},
		{name: "number against boolean", stored: `{"n":1}`, submitted: `{"n":true}`},
		{name: "number against null", stored: `{"n":0}`, submitted: `{"n":null}`},
		{name: "null against absent key", stored: `{"n":null}`, submitted: `{}`},
		{name: "extra key", stored: `{"n":1}`, submitted: `{"n":1,"m":1}`},
		{name: "array length", stored: `{"n":[1,2]}`, submitted: `{"n":[1,2,3]}`},
		{name: "array order", stored: `{"n":[1,2]}`, submitted: `{"n":[2,1]}`},
		{name: "object against array", stored: `{"n":{}}`, submitted: `{"n":[]}`},
		{name: "string differs", stored: `{"s":"a"}`, submitted: `{"s":"b"}`},
		{name: "invalid stored", stored: `{`, submitted: `{}`},
		{name: "invalid submitted", stored: `{}`, submitted: `{`},
		{name: "trailing document", stored: `{}`, submitted: `{} {}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			got := samePayload(json.RawMessage(testCase.stored), json.RawMessage(testCase.submitted))
			if got != testCase.want {
				subTest.Fatalf("samePayload(%s, %s) = %v, want %v", testCase.stored, testCase.submitted, got, testCase.want)
			}
			// Equality is symmetric.
			if reverse := samePayload(json.RawMessage(testCase.submitted), json.RawMessage(testCase.stored)); reverse != testCase.want {
				subTest.Fatalf("samePayload(%s, %s) = %v, want %v", testCase.submitted, testCase.stored, reverse, testCase.want)
			}
		})
	}
}

func TestSameDisplayTreatsNilAsTheZeroDisplay(testInstance *testing.T) {
	testCases := []struct {
		name      string
		stored    *Display
		submitted *Display
		want      bool
	}{
		{name: "both nil", want: true},
		{name: "both empty", stored: &Display{}, submitted: &Display{}, want: true},
		{name: "nil against empty", submitted: &Display{}, want: true},
		{name: "empty against nil", stored: &Display{}, want: true},
		{name: "equal content", stored: &Display{Title: "T"}, submitted: &Display{Title: "T"}, want: true},
		{name: "nil against content", submitted: &Display{Title: "T"}},
		{name: "content against nil", stored: &Display{Title: "T"}},
		{name: "different content", stored: &Display{Title: "T"}, submitted: &Display{Title: "U"}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			if got := sameDisplay(testCase.stored, testCase.submitted); got != testCase.want {
				subTest.Fatalf("sameDisplay(%+v, %+v) = %v, want %v", testCase.stored, testCase.submitted, got, testCase.want)
			}
		})
	}
}
