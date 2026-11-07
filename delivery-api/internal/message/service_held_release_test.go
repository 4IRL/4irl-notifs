package message

import (
	"context"
	"time"
)

// heldReleaseCall records one ReleaseHeldMessage call.
type heldReleaseCall struct {
	messageID string
	targets   []DeliveryTarget
	now       time.Time
}

// heldFailCall records one FailHeldRelease call.
type heldFailCall struct {
	messageID string
	reason    string
	now       time.Time
}

// heldPairsCall records one ListHeldPairs call.
type heldPairsCall struct {
	after HeldPair
	limit int
}

// heldReleaseStoreScript holds the scripted results of the held-release store
// methods; it is embedded in fakeServiceStore. The pages and results are
// consumed one per call; the last one repeats.
type heldReleaseStoreScript struct {
	heldPairsPages []([]HeldPair)
	heldPairsErr   error
	heldPairsCalls []heldPairsCall

	releasableResult []StoredMessage
	releasableErr    error
	releasableCalls  [][]HeldPair

	// releaseResults maps a message id to its scripted result; an unlisted id
	// returns HeldReleased.
	releaseResults map[string]HeldReleaseResult
	releaseErrs    map[string]error
	releaseCalls   []heldReleaseCall

	// failResults maps a message id to its scripted result; an unlisted id
	// returns true.
	failSkipped map[string]bool
	failErrs    map[string]error
	failCalls   []heldFailCall

	failuresResult []HeldFailure
	failuresTotal  int
	failuresErr    error
	failuresCalls  []int
}

func (fake *fakeServiceStore) ListHeldPairs(_ context.Context, after HeldPair, limit int) ([]HeldPair, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.heldPairsCalls = append(fake.heldPairsCalls, heldPairsCall{after: after, limit: limit})
	if fake.heldPairsErr != nil {
		return nil, fake.heldPairsErr
	}
	if len(fake.heldPairsPages) == 0 {
		return []HeldPair{}, nil
	}
	index := min(len(fake.heldPairsCalls)-1, len(fake.heldPairsPages)-1)
	return fake.heldPairsPages[index], nil
}

func (fake *fakeServiceStore) ListReleasableHeld(_ context.Context, pairs []HeldPair, _ int) ([]StoredMessage, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.releasableCalls = append(fake.releasableCalls, pairs)
	if fake.releasableResult == nil {
		return []StoredMessage{}, fake.releasableErr
	}
	return fake.releasableResult, fake.releasableErr
}

func (fake *fakeServiceStore) ReleaseHeldMessage(
	_ context.Context, messageID string, targets []DeliveryTarget, now time.Time,
) (HeldReleaseResult, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.releaseCalls = append(fake.releaseCalls, heldReleaseCall{messageID: messageID, targets: targets, now: now})
	if err := fake.releaseErrs[messageID]; err != nil {
		return "", err
	}
	if result, scripted := fake.releaseResults[messageID]; scripted {
		return result, nil
	}
	return HeldReleased, nil
}

func (fake *fakeServiceStore) FailHeldRelease(
	_ context.Context, messageID string, reason string, now time.Time,
) (bool, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.failCalls = append(fake.failCalls, heldFailCall{messageID: messageID, reason: reason, now: now})
	if err := fake.failErrs[messageID]; err != nil {
		return false, err
	}
	return !fake.failSkipped[messageID], nil
}

func (fake *fakeServiceStore) ListHeldFailures(_ context.Context, limit int) ([]HeldFailure, int, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.failuresCalls = append(fake.failuresCalls, limit)
	if fake.failuresResult == nil {
		return []HeldFailure{}, fake.failuresTotal, fake.failuresErr
	}
	return fake.failuresResult, fake.failuresTotal, fake.failuresErr
}
