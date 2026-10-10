package migrate

import (
	"strings"
	"testing"
)

func TestTruncateRunes(testInstance *testing.T) {
	testCases := []struct {
		name      string
		text      string
		wantRunes int
	}{
		{name: "ascii over limit", text: strings.Repeat("x", 600), wantRunes: 500},
		{name: "multibyte over limit", text: strings.Repeat("é", 600), wantRunes: 500},
		{name: "exactly at limit", text: strings.Repeat("y", 500), wantRunes: 500},
		{name: "under limit is untouched", text: "short", wantRunes: 5},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			got := truncateRunes(testCase.text, maxRecordedFailureRunes)

			if gotRunes := len([]rune(got)); gotRunes != testCase.wantRunes {
				subTest.Fatalf("truncated to %d runes, want %d", gotRunes, testCase.wantRunes)
			}
			if !strings.HasPrefix(testCase.text, got) {
				subTest.Fatalf("truncated text is not a prefix of the input")
			}
		})
	}
}
