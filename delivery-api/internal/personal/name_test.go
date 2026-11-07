package personal

import "testing"

func TestPersonalDeliveryNameRoundTrip(testInstance *testing.T) {
	for _, id := range []int64{1, 7, 42, 9223372036854775807} {
		name := PersonalDeliveryName(id)
		parsed, ok := ParsePersonalDeliveryName(name)
		if !ok || parsed != id {
			testInstance.Errorf("round trip of %d via %q = (%d, %v)", id, name, parsed, ok)
		}
	}
	if got := PersonalDeliveryName(12); got != "@personal_12" {
		testInstance.Errorf("PersonalDeliveryName(12) = %q, want @personal_12", got)
	}
}

func TestParsePersonalDeliveryNameRejectsNonCanonicalForms(testInstance *testing.T) {
	testCases := []struct {
		label string
		name  string
	}{
		{label: "leading zeros", name: "@personal_007"},
		{label: "zero", name: "@personal_0"},
		{label: "plus sign", name: "@personal_+5"},
		{label: "negative", name: "@personal_-1"},
		{label: "empty id", name: "@personal_"},
		{label: "overflow past int64", name: "@personal_9223372036854775808"},
		{label: "trailing text", name: "@personal_5x"},
		{label: "trailing space", name: "@personal_5 "},
		{label: "wrong prefix", name: "@personnel_5"},
		{label: "no at sign", name: "personal_5"},
		{label: "recipient name", name: "@recipient"},
		{label: "operator-style name", name: "ops_webhook"},
		{label: "empty", name: ""},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			if id, ok := ParsePersonalDeliveryName(testCase.name); ok {
				subTest.Fatalf("ParsePersonalDeliveryName(%q) = (%d, true), want rejection", testCase.name, id)
			}
		})
	}
}

func TestIsPersonalDeliveryName(testInstance *testing.T) {
	for name, want := range map[string]bool{
		"@personal_5": true, "@personal_007": true, "@personal_": true, "@recipient": false, "ops": false,
	} {
		if got := IsPersonalDeliveryName(name); got != want {
			testInstance.Errorf("IsPersonalDeliveryName(%q) = %v, want %v", name, got, want)
		}
	}
}
