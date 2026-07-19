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
			name:     "access grant read-only scoped topic pattern",
			got:      AccessGrantArgs("u_76gzqgp4byjl6dje", "urls4irl-76gzqgp4byjl6dje-*", PermissionReadOnly),
			expected: []string{"access", "u_76gzqgp4byjl6dje", "urls4irl-76gzqgp4byjl6dje-*", "ro"},
		},
		{
			name:     "access reset scoped topic pattern",
			got:      AccessResetArgs("u_76gzqgp4byjl6dje", "urls4irl-76gzqgp4byjl6dje-*"),
			expected: []string{"access", "--reset", "u_76gzqgp4byjl6dje", "urls4irl-76gzqgp4byjl6dje-*"},
		},
		{
			name:     "token add labeled with app id",
			got:      TokenAddArgs("u_76gzqgp4byjl6dje", "urls4irl"),
			expected: []string{"token", "add", "--label", "urls4irl", "u_76gzqgp4byjl6dje"},
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
		name       string
		appID      string
		personHash string
		expected   string
	}{
		{name: "urls4irl", appID: "urls4irl", personHash: "76gzqgp4byjl6dje", expected: "urls4irl-76gzqgp4byjl6dje-*"},
		{name: "chores4irl", appID: "chores4irl", personHash: "abcdefgh23456777", expected: "chores4irl-abcdefgh23456777-*"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := TopicPattern(testCase.appID, testCase.personHash); got != testCase.expected {
				t.Fatalf("TopicPattern(%q, %q) = %q, expected %q", testCase.appID, testCase.personHash, got, testCase.expected)
			}
		})
	}
}

func TestPermissionReadOnlyValue(t *testing.T) {
	if PermissionReadOnly != "ro" {
		t.Fatalf("PermissionReadOnly = %q, expected %q", PermissionReadOnly, "ro")
	}
}
