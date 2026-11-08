package message

import (
	"context"
	"errors"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// heldReleasePairPage is how many distinct held (app, channel) pairs one
// ReleaseHeld batch resolves.
const heldReleasePairPage = 100

// heldReleaseWarnInterval is the minimum gap between two throttled held
// release warnings with the same key.
const heldReleaseWarnInterval = 10 * time.Minute

// Error classes used in throttle keys and in the pair-level screen.
const (
	heldClassUnsupportedType = "unsupported_type"
	heldClassNoPublisher     = "no_publisher"
	heldClassOther           = "error"
)

// ReleaseHeld runs one held-release batch: it resolves a page of held pairs
// once each, releases the held messages of the resolvable pairs oldest first
// (up to limit), and terminally fails the ones that can never be delivered as
// written. Anything transient leaves the message held. It returns released plus
// terminal-failed messages, so a short count ends RunReleaser's drain. It
// satisfies dispatch.ReleaserFunc.
func (service *Service) ReleaseHeld(ctx context.Context, now time.Time, limit int) (int64, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	if limit <= 0 {
		return 0, nil
	}
	pairs, pairsErr := service.nextHeldPairs(ctx)
	if pairsErr != nil {
		return 0, pairsErr
	}
	resolvable, resolveErr := service.resolveHeldPairs(ctx, pairs, now)
	if resolveErr != nil {
		return 0, resolveErr
	}
	service.advanceHeldCursor(pairs)
	if len(resolvable) == 0 {
		return 0, nil
	}
	pairList := make([]HeldPair, 0, len(resolvable))
	for _, pair := range pairs {
		if _, ok := resolvable[pair]; ok {
			pairList = append(pairList, pair)
		}
	}
	candidates, listErr := service.store.ListReleasableHeld(ctx, pairList, limit)
	if listErr != nil {
		return 0, listErr
	}

	var released, failed int64
	var oldestHeldAt *time.Time
	for _, candidate := range candidates {
		if ctxErr := ctx.Err(); ctxErr != nil {
			service.logReleaseSummary(released, failed, oldestHeldAt, now)
			return released + failed, ctxErr
		}
		outcome, outcomeErr := service.releaseOneHeld(ctx, candidate, resolvable[HeldPair{
			AppID: candidate.Envelope.AppID, Channel: candidate.Envelope.Channel,
		}], now)
		if outcomeErr != nil {
			service.logReleaseSummary(released, failed, oldestHeldAt, now)
			return released + failed, outcomeErr
		}
		switch outcome {
		case heldOutcomeReleased:
			released++
			if candidate.HeldAt != nil && (oldestHeldAt == nil || candidate.HeldAt.Before(*oldestHeldAt)) {
				oldestHeldAt = candidate.HeldAt
			}
		case heldOutcomeFailed:
			failed++
		case heldOutcomeStayed:
		}
	}
	service.logReleaseSummary(released, failed, oldestHeldAt, now)
	return released + failed, nil
}

// heldOutcome is what ReleaseHeld did with one candidate.
type heldOutcome int

const (
	// heldOutcomeStayed means the message was left held or skipped: not counted.
	heldOutcomeStayed heldOutcome = iota
	heldOutcomeReleased
	heldOutcomeFailed
)

// nextHeldPairs reads the next page of held pairs after the service cursor. It
// does not move the cursor: advanceHeldCursor does, once the page resolved.
func (service *Service) nextHeldPairs(ctx context.Context) ([]HeldPair, error) {
	service.heldCursorMutex.Lock()
	defer service.heldCursorMutex.Unlock()
	pairs, listErr := service.store.ListHeldPairs(ctx, service.heldCursor, heldReleasePairPage)
	if listErr != nil {
		return nil, cancelOr(ctx, listErr)
	}
	return pairs, nil
}

// advanceHeldCursor moves the cursor past a resolved page, wrapping to the start
// after a short page. A cancelled or failed batch never calls it, so its page is
// read again next time.
func (service *Service) advanceHeldCursor(pairs []HeldPair) {
	service.heldCursorMutex.Lock()
	defer service.heldCursorMutex.Unlock()
	if len(pairs) < heldReleasePairPage {
		service.heldCursor = HeldPair{}
		return
	}
	service.heldCursor = pairs[len(pairs)-1]
}

// resolveHeldPairs resolves each pair once and returns the ordered destinations
// of the pairs that can release. A pair with no route yet is a Debug line; a
// real fault (resolve error, or a destination type with no sink) is a throttled
// Warn. Both exclude the pair from the batch.
func (service *Service) resolveHeldPairs(
	ctx context.Context, pairs []HeldPair, now time.Time,
) (map[HeldPair][]registry.ResolvedDestination, error) {
	resolvable := make(map[HeldPair][]registry.ResolvedDestination, len(pairs))
	for _, pair := range pairs {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		destinations, resolveErr := service.resolveRouteDestinations(ctx, Envelope{
			AppID: pair.AppID, Channel: pair.Channel,
		})
		if resolveErr != nil {
			if cancelErr := cancelFrom(ctx, resolveErr); cancelErr != nil {
				return nil, cancelErr
			}
			if errors.Is(resolveErr, ErrNoDestinationMatched) {
				service.logger.Debug("held pair has no route yet", "app_id", pair.AppID, "channel", pair.Channel)
				continue
			}
			service.warnHeldPair(pair, heldErrorClass(resolveErr), resolveErr, now)
			continue
		}
		if !service.allTypesSupported(destinations) {
			service.warnHeldPair(pair, heldClassUnsupportedType, ErrUnsupportedDestinationType, now)
			continue
		}
		resolvable[pair] = destinations
	}
	return resolvable, nil
}

// allTypesSupported is the release-only pair screen: every destination's type
// must have a sink.
func (service *Service) allTypesSupported(destinations []registry.ResolvedDestination) bool {
	for _, destination := range destinations {
		if !service.supportedTypes[destination.Type] {
			return false
		}
	}
	return true
}

// releaseOneHeld classifies and writes one candidate. A non-nil error is only a
// context cancellation, which ends the batch; every other failure leaves the
// message held (or skipped) and is logged under the throttle.
func (service *Service) releaseOneHeld(
	ctx context.Context, candidate StoredMessage, destinations []registry.ResolvedDestination, now time.Time,
) (heldOutcome, error) {
	if candidate.Envelope.ToDestination != "" || candidate.Envelope.ToRecipient != "" {
		service.warnHeldMessage(candidate.ID, errors.New("held message carries a direct address; left held"), now)
		return heldOutcomeStayed, nil
	}
	targets, checkErr := service.routeTargets(candidate.Envelope, destinations)
	if checkErr != nil {
		var validationErr *ValidationError
		if errors.As(checkErr, &validationErr) {
			return service.failHeld(ctx, candidate, now)
		}
		service.warnHeldMessage(candidate.ID, checkErr, now)
		return heldOutcomeStayed, nil
	}
	result, releaseErr := service.store.ReleaseHeldMessage(ctx, candidate.ID, targets, now)
	if releaseErr != nil {
		if cancelErr := cancelFrom(ctx, releaseErr); cancelErr != nil {
			return heldOutcomeStayed, cancelErr
		}
		service.warnHeldMessage(candidate.ID, releaseErr, now)
		return heldOutcomeStayed, nil
	}
	switch result {
	case HeldReleased:
		service.wake()
		return heldOutcomeReleased, nil
	case HeldScheduled:
		return heldOutcomeReleased, nil
	case HeldSkipped:
		return heldOutcomeStayed, nil
	default:
		if service.warnThrottle.Allow("message/"+candidate.ID+"/unknown_result", now) {
			service.logger.Error("held message release returned an unknown result",
				"message_id", candidate.ID, "result", string(result))
		}
		return heldOutcomeStayed, nil
	}
}

// failHeld terminally fails a candidate whose check found a *ValidationError.
func (service *Service) failHeld(ctx context.Context, candidate StoredMessage, now time.Time) (heldOutcome, error) {
	failed, failErr := service.store.FailHeldRelease(ctx, candidate.ID, ReleaseFailedReasonInvalid, now)
	if failErr != nil {
		if cancelErr := cancelFrom(ctx, failErr); cancelErr != nil {
			return heldOutcomeStayed, cancelErr
		}
		service.warnHeldMessage(candidate.ID, failErr, now)
		return heldOutcomeStayed, nil
	}
	if !failed {
		return heldOutcomeStayed, nil
	}
	service.logger.Info("held message release failed",
		"message_id", candidate.ID, "app_id", candidate.Envelope.AppID,
		"channel", candidate.Envelope.Channel, "reason", ReleaseFailedReasonInvalid)
	return heldOutcomeFailed, nil
}

func (service *Service) logReleaseSummary(released, failed int64, oldestHeldAt *time.Time, now time.Time) {
	if released == 0 {
		return
	}
	var oldestAge time.Duration
	if oldestHeldAt != nil {
		oldestAge = now.Sub(*oldestHeldAt)
	}
	service.logger.Info("held messages released",
		"released", released, "failed", failed, "oldest_held_age", oldestAge.String())
}

// warnHeldPair logs a throttled Warn for a pair-level fault.
func (service *Service) warnHeldPair(pair HeldPair, class string, cause error, now time.Time) {
	if !service.warnThrottle.Allow(pair.AppID+"/"+pair.Channel+"/"+class, now) {
		return
	}
	service.logger.Warn("held pair cannot release",
		"app_id", pair.AppID, "channel", pair.Channel, "error", cause)
}

// warnHeldMessage logs a throttled Warn for a per-message fault.
func (service *Service) warnHeldMessage(messageID string, cause error, now time.Time) {
	if !service.warnThrottle.Allow(messageID, now) {
		return
	}
	service.logger.Warn("held message cannot release", "message_id", messageID, "error", cause)
}

// heldErrorClass names an error for the throttle key.
func heldErrorClass(err error) string {
	switch {
	case errors.Is(err, ErrUnsupportedDestinationType):
		return heldClassUnsupportedType
	case errors.Is(err, registry.ErrNoPublisher):
		return heldClassNoPublisher
	default:
		return heldClassOther
	}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// cancelFrom reports whether a non-nil err is a cancellation: the context's own
// error when it is done (even if err does not wrap it), else err when it is a
// context error, else nil.
func cancelFrom(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isContextError(err) {
		return err
	}
	return nil
}

// cancelOr returns the context's error when it is done, else err.
func cancelOr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
