package ntfycli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sentinel errors classifying well-known ntfy CLI failures so callers can
// branch (e.g. tolerate "already exists" during provisioning).
var (
	ErrAlreadyExists = errors.New("ntfy resource already exists")
	ErrNotFound      = errors.New("ntfy resource does not exist")
)

const (
	// defaultMaxAttempts bounds how many times a busy write is attempted.
	defaultMaxAttempts = 5
	// defaultBaseBackoff is the first retry delay; it doubles per attempt.
	defaultBaseBackoff = 50 * time.Millisecond
)

// busyMarkers are the stderr substrings indicating the SQLite auth database
// was locked by a concurrent writer (e.g. the live ntfy server) — the one
// failure class that is safe and useful to retry.
var busyMarkers = []string{"database is locked", "database locked", "SQLITE_BUSY"}

// isBusyStderr reports whether stderr indicates a retryable SQLite-busy failure.
func isBusyStderr(stderr string) bool {
	for _, marker := range busyMarkers {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

// classifyStderr maps well-known CLI failure messages onto sentinel errors;
// it returns nil when stderr matches no known class.
func classifyStderr(stderr string) error {
	if strings.Contains(stderr, "already exists") {
		return ErrAlreadyExists
	}
	if strings.Contains(stderr, "does not exist") {
		return ErrNotFound
	}
	return nil
}

// Runner is the subprocess boundary: it executes one ntfy CLI invocation and
// returns its stdout, stderr, and process error. It exists so the command
// construction, serialization, and retry policy are testable without a real
// ntfy binary.
type Runner interface {
	Run(ctx context.Context, args []string, extraEnv []string) (stdout string, stderr string, err error)
}

// ClientConfig configures a Client. Runner is required; the remaining fields
// default to production values when zero.
type ClientConfig struct {
	Runner Runner
	// Sleep is called between busy retries; injectable for tests.
	Sleep func(duration time.Duration)
}

// Client wraps the ntfy CLI with the policies the provisioning API needs:
// every invocation is serialized behind one mutex (the SQLite auth database
// has a single writer), and writes that race the live server's own writes are
// retried with backoff when SQLite reports the database is busy/locked.
type Client struct {
	runner Runner
	sleep  func(duration time.Duration)
	mutex  sync.Mutex
}

// NewClient builds a Client from config, applying production defaults.
func NewClient(config ClientConfig) *Client {
	sleepFunc := config.Sleep
	if sleepFunc == nil {
		sleepFunc = time.Sleep
	}
	return &Client{runner: config.Runner, sleep: sleepFunc}
}

// run executes one CLI invocation under the serialization mutex, retrying
// with exponential backoff when the auth database reports busy/locked.
func (client *Client) run(ctx context.Context, args []string, extraEnv []string) (string, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	backoff := defaultBaseBackoff
	var stdout, stderr string
	var runErr error
	for attempt := 1; attempt <= defaultMaxAttempts; attempt++ {
		stdout, stderr, runErr = client.runner.Run(ctx, args, extraEnv)
		if runErr == nil {
			return stdout, nil
		}
		if !isBusyStderr(stderr) {
			break
		}
		if attempt < defaultMaxAttempts {
			client.sleep(backoff)
			backoff *= 2
		}
	}
	if sentinel := classifyStderr(stderr); sentinel != nil {
		return stdout, fmt.Errorf("ntfy %s: %w: %s", args[0], sentinel, stderr)
	}
	return stdout, fmt.Errorf("ntfy %s: %w: %s", args[0], runErr, stderr)
}

// AddUser creates a user with the given password (passed via NTFY_PASSWORD,
// never argv, so it cannot leak through the process table).
func (client *Client) AddUser(ctx context.Context, userID string, password string) error {
	_, runErr := client.run(ctx, UserAddArgs(userID), []string{"NTFY_PASSWORD=" + password})
	return runErr
}

// DeleteUser removes a user; ntfy also drops the user's ACL entries and tokens.
func (client *Client) DeleteUser(ctx context.Context, userID string) error {
	_, runErr := client.run(ctx, UserDeleteArgs(userID), nil)
	return runErr
}

// GrantAccess grants permission to userID on topicPattern.
func (client *Client) GrantAccess(ctx context.Context, userID string, topicPattern string, permission Permission) error {
	_, runErr := client.run(ctx, AccessGrantArgs(userID, topicPattern, permission), nil)
	return runErr
}

// ResetAccess revokes userID's access to topicPattern.
func (client *Client) ResetAccess(ctx context.Context, userID string, topicPattern string) error {
	_, runErr := client.run(ctx, AccessResetArgs(userID, topicPattern), nil)
	return runErr
}

// RemoveToken deletes a single token belonging to the user.
func (client *Client) RemoveToken(ctx context.Context, userID string, token string) error {
	_, runErr := client.run(ctx, TokenRemoveArgs(userID, token), nil)
	return runErr
}

// User is one entry from `ntfy user list`: the username plus the topic
// patterns the user has been granted access to.
type User struct {
	Name          string
	TopicPatterns []string
}

// userListHeaderPattern matches a user-block header of `ntfy user list`
// output, e.g. "user alice (role: user, tier: none)".
var userListHeaderPattern = regexp.MustCompile(`^user (\S+) \(role: ([a-z]+)`)

// userListAccessPattern matches an access line of a user block, e.g.
// "- read-write access to topic urls4irl-*".
var userListAccessPattern = regexp.MustCompile(`^- [a-z-]+ access to topic (\S+)$`)

// anonymousUserName is ntfy's synthetic everyone/anonymous user entry.
const anonymousUserName = "*"

// ListUsers returns every real user with their granted topic patterns,
// excluding ntfy's synthetic anonymous ("*") entry.
func (client *Client) ListUsers(ctx context.Context) ([]User, error) {
	stdout, runErr := client.run(ctx, UserListArgs(), nil)
	if runErr != nil {
		return nil, runErr
	}

	var users []User
	var currentUser *User
	for _, line := range strings.Split(stdout, "\n") {
		trimmedLine := strings.TrimSpace(line)
		if headerMatch := userListHeaderPattern.FindStringSubmatch(trimmedLine); headerMatch != nil {
			if currentUser != nil {
				users = append(users, *currentUser)
			}
			if headerMatch[1] == anonymousUserName {
				currentUser = nil
				continue
			}
			currentUser = &User{Name: headerMatch[1]}
			continue
		}
		if currentUser == nil {
			continue
		}
		if accessMatch := userListAccessPattern.FindStringSubmatch(trimmedLine); accessMatch != nil {
			currentUser.TopicPatterns = append(currentUser.TopicPatterns, accessMatch[1])
		}
	}
	if currentUser != nil {
		users = append(users, *currentUser)
	}
	return users, nil
}

// Token is one entry from `ntfy token list`: the token value plus the label
// it was created with (empty when unlabeled).
type Token struct {
	Value string
	Label string
}

// tokenListLinePattern matches one token line of `ntfy token list` output,
// e.g. "- tk_abc... (urls4irl), never expires, ..." (label optional).
var tokenListLinePattern = regexp.MustCompile(`^- (tk_[A-Za-z0-9_]+)(?: \(([^)]*)\))?,`)

// ListTokens returns the user's tokens with their labels.
func (client *Client) ListTokens(ctx context.Context, userID string) ([]Token, error) {
	stdout, runErr := client.run(ctx, TokenListArgs(userID), nil)
	if runErr != nil {
		return nil, runErr
	}

	var tokens []Token
	for _, line := range strings.Split(stdout, "\n") {
		match := tokenListLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		tokens = append(tokens, Token{Value: match[1], Label: match[2]})
	}
	return tokens, nil
}

// tokenStdoutPattern matches the CLI's token-creation confirmation, e.g.
// "token tk_abc... created for user alice, never expires".
var tokenStdoutPattern = regexp.MustCompile(`\btoken (tk_[A-Za-z0-9_]+) created for user `)

// AddToken creates a never-expiring token labeled with label (subscriber
// tokens are labeled with the app_id) and returns the token value parsed
// from the CLI confirmation line.
func (client *Client) AddToken(ctx context.Context, userID string, label string) (string, error) {
	stdout, runErr := client.run(ctx, TokenAddArgs(userID, label), nil)
	if runErr != nil {
		return "", runErr
	}
	match := tokenStdoutPattern.FindStringSubmatch(stdout)
	if match == nil {
		return "", fmt.Errorf("ntfy token: could not parse token from output %q", stdout)
	}
	return match[1], nil
}
