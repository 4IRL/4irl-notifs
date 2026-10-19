package message

import (
	"bytes"
	"encoding/json"
	"math/big"
	"time"
)

// sameContent reports whether a submitted envelope carries the same content as
// the stored message, so an identical retry of a scheduled submit is a no-op
// rather than a replace. Both envelopes must already have their recipient
// hashed. It compares channel, to.destination, to.recipient, critical,
// deliver_at (as an instant, to the microsecond Postgres stores, any offset),
// payload and display. The resolved destination set is deliberately not part of
// it. When in doubt it answers false, which only costs an extra replace.
func sameContent(stored Envelope, submitted Envelope) bool {
	return stored.Channel == submitted.Channel &&
		stored.ToDestination == submitted.ToDestination &&
		stored.ToRecipient == submitted.ToRecipient &&
		stored.Critical == submitted.Critical &&
		sameDeliverAt(stored.DeliverAt, submitted.DeliverAt) &&
		samePayload(stored.Payload, submitted.Payload) &&
		sameDisplay(stored.Display, submitted.Display)
}

// sameDeliverAt compares two optional instants, truncating to microseconds
// because that is all the store keeps.
func sameDeliverAt(stored *time.Time, submitted *time.Time) bool {
	if stored == nil || submitted == nil {
		return stored == nil && submitted == nil
	}
	return stored.Truncate(time.Microsecond).Equal(submitted.Truncate(time.Microsecond))
}

// sameDisplay compares two optional displays; nil equals the zero Display.
func sameDisplay(stored *Display, submitted *Display) bool {
	var storedValue, submittedValue Display
	if stored != nil {
		storedValue = *stored
	}
	if submitted != nil {
		submittedValue = *submitted
	}
	return storedValue == submittedValue
}

// samePayload compares two JSON documents by value, ignoring key order and
// whitespace. Numbers are compared by their exact numeric value (big.Rat over
// the json.Number literal), never through float64: two integers beyond float64
// precision stay unequal, while spellings of one number are equal (1e2, 100 and
// 100.0; -0 and 0; 1E-2 and 0.01). That matters because the stored side comes
// back from jsonb, which rewrites numeric literals, so a byte-identical retry
// must still match what was stored. A document that does not decode compares
// unequal.
func samePayload(stored json.RawMessage, submitted json.RawMessage) bool {
	storedValue, storedOK := decodeJSONValue(stored)
	submittedValue, submittedOK := decodeJSONValue(submitted)
	return storedOK && submittedOK && sameJSONValue(storedValue, submittedValue)
}

// sameJSONValue compares two values decoded by decodeJSONValue: objects by key,
// arrays by position, numbers by exact value, and the rest by equality.
func sameJSONValue(left any, right any) bool {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, leftItem := range leftValue {
			rightItem, present := rightValue[key]
			if !present || !sameJSONValue(leftItem, rightItem) {
				return false
			}
		}
		return true
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !sameJSONValue(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case json.Number:
		rightValue, ok := right.(json.Number)
		return ok && sameJSONNumber(leftValue, rightValue)
	default:
		// string, bool and nil: comparable, and a mismatched type is unequal.
		return left == right
	}
}

// sameJSONNumber reports whether two JSON number literals denote the same exact
// value. A literal that does not parse as a rational (big.Rat caps the
// exponent) is only equal to an identical literal.
func sameJSONNumber(left json.Number, right json.Number) bool {
	if left == right {
		return true
	}
	leftValue, leftOK := new(big.Rat).SetString(string(left))
	rightValue, rightOK := new(big.Rat).SetString(string(right))
	return leftOK && rightOK && leftValue.Cmp(rightValue) == 0
}

// decodeJSONValue decodes one JSON document with numbers kept as json.Number.
func decodeJSONValue(document json.RawMessage) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if decodeErr := decoder.Decode(&value); decodeErr != nil {
		return nil, false
	}
	if decoder.More() {
		return nil, false
	}
	return value, true
}
