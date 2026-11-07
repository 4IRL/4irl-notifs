package message

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestListHeldFailuresValidatesLimitAndPassesTotalThrough(testInstance *testing.T) {
	testInstance.Run("rejects limits outside 1..200 before the store", func(subTest *testing.T) {
		fixture := newServiceFixture()
		for _, limit := range []int{0, -1, MaxListLimit + 1} {
			_, _, err := fixture.service.ListHeldFailures(context.Background(), limit)
			wantValidationMessage(subTest, err, "invalid limit")
		}
		if len(fixture.store.failuresCalls) != 0 {
			subTest.Fatalf("store calls = %d, want 0", len(fixture.store.failuresCalls))
		}
	})

	testInstance.Run("accepts the boundaries and passes total through", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.failuresResult = []HeldFailure{{MessageID: existingMessageID, AppID: "urls4irl"}}
		fixture.store.failuresTotal = 312
		for _, limit := range []int{1, DefaultListLimit, MaxListLimit} {
			listed, total, err := fixture.service.ListHeldFailures(context.Background(), limit)
			if err != nil || total != 312 || len(listed) != 1 {
				subTest.Fatalf("ListHeldFailures(%d) = (%d failures, total %d, %v), want 1 failure and total 312",
					limit, len(listed), total, err)
			}
		}
		if want := []int{1, DefaultListLimit, MaxListLimit}; !reflect.DeepEqual(fixture.store.failuresCalls, want) {
			subTest.Fatalf("store limits = %v, want %v", fixture.store.failuresCalls, want)
		}
	})

	testInstance.Run("never returns nil without an error", func(subTest *testing.T) {
		fixture := newServiceFixture()
		listed, total, err := fixture.service.ListHeldFailures(context.Background(), DefaultListLimit)
		if err != nil || listed == nil || total != 0 {
			subTest.Fatalf("ListHeldFailures = (%v, %d, %v), want an empty non-nil slice", listed, total, err)
		}
	})

	testInstance.Run("propagates store errors", func(subTest *testing.T) {
		storeErr := errors.New("db down")
		fixture := newServiceFixture()
		fixture.store.failuresErr = storeErr
		if _, _, err := fixture.service.ListHeldFailures(context.Background(), DefaultListLimit); !errors.Is(err, storeErr) {
			subTest.Fatalf("ListHeldFailures error = %v, want %v", err, storeErr)
		}
	})
}
