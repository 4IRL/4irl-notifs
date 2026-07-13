package ntfycli

import (
	"reflect"
	"testing"
)

func TestArgBuilders(t *testing.T) {
	testCases := []struct {
		name     string
		got      []string
		expected []string
	}{
		{
			name:     "user add",
			got:      UserAddArgs("alice"),
			expected: []string{"user", "add", "alice"},
		},
		{
			name:     "user delete",
			got:      UserDeleteArgs("alice"),
			expected: []string{"user", "del", "alice"},
		},
		{
			name:     "user list",
			got:      UserListArgs(),
			expected: []string{"user", "list"},
		},
		{
			name:     "access grant read-write wildcard for app",
			got:      AccessGrantArgs("alice", "urls4irl"),
			expected: []string{"access", "alice", "urls4irl-*", "rw"},
		},
		{
			name:     "access reset for app",
			got:      AccessResetArgs("alice", "urls4irl"),
			expected: []string{"access", "--reset", "alice", "urls4irl-*"},
		},
		{
			name:     "token add labeled with app id",
			got:      TokenAddArgs("alice", "urls4irl"),
			expected: []string{"token", "add", "--label", "urls4irl", "alice"},
		},
		{
			name:     "token list for user",
			got:      TokenListArgs("alice"),
			expected: []string{"token", "list", "alice"},
		},
		{
			name:     "token remove",
			got:      TokenRemoveArgs("alice", "tk_abc123"),
			expected: []string{"token", "remove", "alice", "tk_abc123"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if !reflect.DeepEqual(testCase.got, testCase.expected) {
				t.Fatalf("got %v, expected %v", testCase.got, testCase.expected)
			}
		})
	}
}

func TestTopicPattern(t *testing.T) {
	testCases := []struct {
		appID    string
		expected string
	}{
		{appID: "urls4irl", expected: "urls4irl-*"},
		{appID: "chores4irl", expected: "chores4irl-*"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.appID, func(t *testing.T) {
			if got := TopicPattern(testCase.appID); got != testCase.expected {
				t.Fatalf("TopicPattern(%q) = %q, expected %q", testCase.appID, got, testCase.expected)
			}
		})
	}
}
