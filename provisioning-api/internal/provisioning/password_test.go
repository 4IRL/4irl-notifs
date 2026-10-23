package provisioning

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personsvc"
)

func TestGenerateRandomPasswordProducesUniqueHexSecrets(t *testing.T) {
	hexPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)

	firstPassword, firstErr := GenerateRandomPassword()
	if firstErr != nil {
		t.Fatalf("GenerateRandomPassword returned unexpected error: %v", firstErr)
	}
	secondPassword, secondErr := GenerateRandomPassword()
	if secondErr != nil {
		t.Fatalf("GenerateRandomPassword returned unexpected error: %v", secondErr)
	}

	if !hexPattern.MatchString(firstPassword) {
		t.Fatalf("password %q is not 64 hex chars (32 bytes of entropy)", firstPassword)
	}
	if firstPassword == secondPassword {
		t.Fatal("two generated passwords were identical — not random")
	}
}

var subscriberPasswordPattern = regexp.MustCompile(`^[a-km-np-z2-9]{4}(-[a-km-np-z2-9]{4}){3}$`)

func TestGenerateSubscriberPasswordFormat(t *testing.T) {
	seen := map[string]bool{}
	for draw := 0; draw < 1000; draw++ {
		password, err := GenerateSubscriberPassword()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(password) != 19 {
			t.Fatalf("length = %d for %q", len(password), password)
		}
		if !subscriberPasswordPattern.MatchString(password) {
			t.Fatalf("%q does not match the format", password)
		}
		if strings.ContainsAny(password, "01ol") {
			t.Fatalf("%q contains an ambiguous symbol", password)
		}
		if seen[password] {
			t.Fatalf("duplicate password %q", password)
		}
		seen[password] = true
	}
}

func TestSubscriberPasswordAlphabetHas32Symbols(t *testing.T) {
	if len(subscriberPasswordAlphabet) != 32 {
		t.Fatalf("alphabet has %d symbols, expected 32", len(subscriberPasswordAlphabet))
	}
}

func TestNewServiceDefaultsSubscriberGeneratorToRealOne(t *testing.T) {
	service := NewService(ServiceConfig{})
	password, err := service.generateSubscriberPassword()
	if err != nil || !subscriberPasswordPattern.MatchString(password) {
		t.Fatalf("password = %q, err = %v", password, err)
	}
}

func TestStatefulFakeIfUnsetClaimsOncePerHashUntilReleased(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersonClient{configuredValue: true, stateful: true}

	first, err := fake.ClaimPassword(ctx, "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimIfUnset)
	if err != nil || !first.Claimed || first.SetAt == "" || !first.RowExists {
		t.Fatalf("first claim = %+v, err = %v", first, err)
	}
	second, _ := fake.ClaimPassword(ctx, "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimIfUnset)
	if second.Claimed || !second.RowExists || second.SetAt != first.SetAt {
		t.Fatalf("second claim = %+v", second)
	}
	other, _ := fake.ClaimPassword(ctx, "hashbbbbbbbbbbbb", "c@d.co", personsvc.ClaimIfUnset)
	if !other.Claimed {
		t.Fatalf("other hash claim = %+v", other)
	}

	if releaseErr := fake.ReleasePasswordClaim(ctx, "hashaaaaaaaaaaaa", first, ""); releaseErr != nil {
		t.Fatalf("release error: %v", releaseErr)
	}
	third, _ := fake.ClaimPassword(ctx, "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimIfUnset)
	if !third.Claimed {
		t.Fatalf("claim after release = %+v", third)
	}
}

func TestStatefulFakeClaimModesAndReleaseIfEqual(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersonClient{configuredValue: true, stateful: true}

	missing, _ := fake.ClaimPassword(ctx, "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimUpdateOnly)
	if missing.Claimed || missing.RowExists {
		t.Fatalf("update_only on missing row = %+v", missing)
	}
	if _, exists := fake.rows["hashaaaaaaaaaaaa"]; exists {
		t.Fatal("update_only created a row")
	}

	created, _ := fake.ClaimPassword(ctx, "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimAlways)
	overwritten, _ := fake.ClaimPassword(ctx, "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimUpdateOnly)
	if !overwritten.Claimed || !overwritten.RowExists || overwritten.PreviousSetAt != created.SetAt || overwritten.SetAt == created.SetAt {
		t.Fatalf("update_only on present row = %+v (created %+v)", overwritten, created)
	}

	// Releasing the older claim must not undo the newer stamp.
	_ = fake.ReleasePasswordClaim(ctx, "hashaaaaaaaaaaaa", created, "")
	if fake.rows["hashaaaaaaaaaaaa"].stamp != overwritten.SetAt {
		t.Fatal("stale release undid a newer claim")
	}
	_ = fake.ReleasePasswordClaim(ctx, "hashaaaaaaaaaaaa", overwritten, overwritten.PreviousSetAt)
	if fake.rows["hashaaaaaaaaaaaa"].stamp != created.SetAt {
		t.Fatal("release did not restore the previous stamp")
	}
}

func TestStatefulFakeConcurrentIfUnsetClaimsExactlyOnce(t *testing.T) {
	fake := &fakePersonClient{configuredValue: true, stateful: true}
	const goroutines = 50
	var waitGroup sync.WaitGroup
	var claimedCount, limitedCount atomic.Int32
	for range goroutines {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			claim, _ := fake.ClaimPassword(context.Background(), "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimIfUnset)
			if claim.Claimed {
				claimedCount.Add(1)
			}
			if _, slotErr := fake.ClaimResetSlot(context.Background(), "hashaaaaaaaaaaaa"); slotErr != nil {
				limitedCount.Add(1)
			}
		}()
	}
	waitGroup.Wait()
	if claimedCount.Load() != 1 {
		t.Fatalf("claimed %d times, expected 1", claimedCount.Load())
	}
	if limitedCount.Load() != goroutines-fakeResetLimit {
		t.Fatalf("limited %d times, expected %d", limitedCount.Load(), goroutines-fakeResetLimit)
	}
}

func TestStatefulFakeRejectsUnknownClaimMode(t *testing.T) {
	fake := &fakePersonClient{configuredValue: true, stateful: true}
	if _, err := fake.ClaimPassword(context.Background(), "hashaaaaaaaaaaaa", "a@b.co", personsvc.ClaimMode("")); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestStatefulFakeResetSlotLimit(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersonClient{configuredValue: true, stateful: true}

	var slotIDs []string
	for index := 0; index < fakeResetLimit; index++ {
		slotID, err := fake.ClaimResetSlot(ctx, "hashaaaaaaaaaaaa")
		if err != nil {
			t.Fatalf("slot %d: %v", index, err)
		}
		slotIDs = append(slotIDs, slotID)
	}
	_, err := fake.ClaimResetSlot(ctx, "hashaaaaaaaaaaaa")
	var limitErr *personsvc.ResetLimitError
	if !errors.Is(err, personsvc.ErrResetLimitReached) || !errors.As(fmt.Errorf("w: %w", err), &limitErr) || limitErr.RetryAfterSeconds <= 0 {
		t.Fatalf("expected limit error, got %v", err)
	}
	if _, otherErr := fake.ClaimResetSlot(ctx, "hashbbbbbbbbbbbb"); otherErr != nil {
		t.Fatalf("other hash limited: %v", otherErr)
	}
	_ = fake.ReleaseResetSlot(ctx, "hashaaaaaaaaaaaa", slotIDs[0])
	if _, afterErr := fake.ClaimResetSlot(ctx, "hashaaaaaaaaaaaa"); afterErr != nil {
		t.Fatalf("claim after release: %v", afterErr)
	}
}
