package message

import "testing"

// sequentialIDs returns count distinct positive ids.
func sequentialIDs(count int) []int64 {
	ids := make([]int64, count)
	for index := range ids {
		ids[index] = int64(index + 1)
	}
	return ids
}

func TestValidateRecipientRouting(testInstance *testing.T) {
	const recipient = "alice@example.com"
	const requiredMessage = "to.personal_destination_ids required for to.via"
	const notAllowedMessage = "to.personal_destination_ids not allowed for to.via ntfy"
	const invalidIDsMessage = "invalid to.personal_destination_ids"
	const needsRecipientMessage = "to.via requires to.recipient"
	testCases := []struct {
		name          string
		toDestination string
		toRecipient   string
		via           RecipientVia
		ids           []int64
		wantMessage   string
	}{
		{name: "unset nil ids", toRecipient: recipient},
		{name: "unset empty ids", toRecipient: recipient, ids: []int64{}},
		{name: "unset with destination and empty ids", toDestination: "ops_webhook", ids: []int64{}},
		{name: "unset route mode"},
		{name: "ntfy nil ids", toRecipient: recipient, via: RecipientViaNtfy},
		{name: "ntfy empty ids", toRecipient: recipient, via: RecipientViaNtfy, ids: []int64{}},
		{name: "personal one id", toRecipient: recipient, via: RecipientViaPersonal, ids: []int64{12}},
		{name: "personal two ids", toRecipient: recipient, via: RecipientViaPersonal, ids: []int64{12, 15}},
		{name: "both one id", toRecipient: recipient, via: RecipientViaBoth, ids: []int64{12}},
		{name: "both two ids", toRecipient: recipient, via: RecipientViaBoth, ids: []int64{12, 15}},
		{name: "max ids", toRecipient: recipient, via: RecipientViaBoth, ids: sequentialIDs(MaxPersonalDestinationsPerMessage)},
		{name: "capitalised", toRecipient: recipient, via: "Both", ids: []int64{1}, wantMessage: "invalid to.via"},
		{name: "unknown", toRecipient: recipient, via: "sms", ids: []int64{1}, wantMessage: "invalid to.via"},
		{name: "leading space", toRecipient: recipient, via: " ntfy", wantMessage: "invalid to.via"},
		{name: "personal nil ids", toRecipient: recipient, via: RecipientViaPersonal, wantMessage: requiredMessage},
		{name: "personal empty ids", toRecipient: recipient, via: RecipientViaPersonal, ids: []int64{}, wantMessage: requiredMessage},
		{name: "both nil ids", toRecipient: recipient, via: RecipientViaBoth, wantMessage: requiredMessage},
		{name: "both empty ids", toRecipient: recipient, via: RecipientViaBoth, ids: []int64{}, wantMessage: requiredMessage},
		{name: "ntfy with ids", toRecipient: recipient, via: RecipientViaNtfy, ids: []int64{1}, wantMessage: notAllowedMessage},
		{name: "unset with ids", toRecipient: recipient, ids: []int64{1}, wantMessage: notAllowedMessage},
		{name: "zero id", toRecipient: recipient, via: RecipientViaPersonal, ids: []int64{0}, wantMessage: invalidIDsMessage},
		{name: "negative id", toRecipient: recipient, via: RecipientViaPersonal, ids: []int64{-1}, wantMessage: invalidIDsMessage},
		{name: "duplicate id", toRecipient: recipient, via: RecipientViaBoth, ids: []int64{12, 12}, wantMessage: invalidIDsMessage},
		{name: "too many ids", toRecipient: recipient, via: RecipientViaBoth, ids: sequentialIDs(MaxPersonalDestinationsPerMessage + 1), wantMessage: invalidIDsMessage},
		{name: "via with destination", toDestination: "ops_webhook", via: RecipientViaNtfy, wantMessage: needsRecipientMessage},
		{name: "via without recipient", via: RecipientViaPersonal, ids: []int64{1}, wantMessage: needsRecipientMessage},
		{name: "ids with destination", toDestination: "ops_webhook", ids: []int64{1}, wantMessage: needsRecipientMessage},
		{name: "ids without recipient", ids: []int64{1}, wantMessage: needsRecipientMessage},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			err := ValidateRecipientRouting(testCase.toDestination, testCase.toRecipient, testCase.via, testCase.ids)
			wantValidationMessage(subTest, err, testCase.wantMessage)
		})
	}
}

func TestRecipientViaOrDefault(testInstance *testing.T) {
	if got := RecipientVia("").OrDefault(); got != RecipientViaNtfy {
		testInstance.Fatalf("unset = %q, want ntfy", got)
	}
	if got := RecipientViaBoth.OrDefault(); got != RecipientViaBoth {
		testInstance.Fatalf("both = %q, want both", got)
	}
}
