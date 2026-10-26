package provisioning

import (
	"context"
	"errors"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/enforcement"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
)

// ErrPersonNotInApp is returned when the person does not hold a grant in the
// calling app. It satisfies errors.Is(err, ntfycli.ErrNotFound), so the HTTP
// layer answers the same 404 "user does not exist" it gives an absent ntfy user
// (and a gateway that rewrites 401/403 never sees this case).
var ErrPersonNotInApp error = &personNotInAppError{}

type personNotInAppError struct{}

func (*personNotInAppError) Error() string {
	return "person is not a subscriber of the calling app"
}

// Is makes errors.Is(err, ntfycli.ErrNotFound) hold.
func (*personNotInAppError) Is(target error) bool {
	return target == ntfycli.ErrNotFound
}

// Routes the grant check guards; the value is logged as the "route" field.
const (
	grantRouteResetPassword = "reset-password"
	grantRouteSetPassword   = "set-password"
	grantRouteDeprovision   = "deprovision"
)

// Values of the "decision" and "reason" fields of the "app grant check" log
// line. The 7-day log-only audit greps journald for would_reject and
// lookup_failed (see ARCHITECTURE.md 'In-app checks').
const (
	grantDecisionAllow        = "allow"
	grantDecisionRejected     = "rejected"
	grantDecisionWouldReject  = "would_reject"
	grantDecisionAlreadyGone  = "already_gone"
	grantDecisionLookupFailed = "lookup_failed"

	grantReasonGrantHeld   = "grant_held"
	grantReasonNoGrant     = "no_grant"
	grantReasonUserMissing = "user_not_found"
	grantReasonLookupError = "lookup_error"
)

// appGrantCheck names the request a grant check is made for.
type appGrantCheck struct {
	route      string
	appID      string
	ntfyUserID string
	personHash string
}

// checkAppGrant verifies, with one GetUser lookup and before any state change,
// that the person holds a grant in the calling app. Every outcome logs one
// "app grant check" line carrying mode, route, app_id, person_hash, decision
// and reason (never an email or password).
//
//   - grant held: allow, nil.
//   - found but no grant: enforce rejects with ErrPersonNotInApp; log-only logs
//     would_reject and returns nil so today's behavior runs.
//   - ntfy user does not exist: Deprovision treats it as an already-gone
//     no-op (alreadyGone true, nil error, in either mode, never logged as a
//     rejection), so per-user deprovision of an absent ntfy user now answers
//     200 {removed:true} where it used to answer 404. ResetPassword/SetPassword reject with
//     ErrPersonNotInApp in both modes, which is the same 404 ChangePassword
//     gives an absent user today, so this is not a behavior change.
//   - any other lookup error: logged as lookup_failed; enforce fails closed by
//     returning the error, log-only returns nil and continues.
//
// Any mode other than log-only enforces, so a future mode value fails closed.
//
// Audit note: because an absent user rejects on reset/set in both modes,
// decision=rejected with reason=user_not_found can appear even in log-only
// mode (it is the same 404 ChangePassword gave an absent user, now answered
// before the rate limiter). The 7-day audit therefore greps only would_reject
// and lookup_failed.
//
// The check is not atomic with the mutation that follows: a concurrent
// deprovision of the same person from the calling app between the lookup and the
// change can still let that one change apply. The caller was a subscriber a
// moment earlier and the password is one per person, so this is accepted.
func (service *Service) checkAppGrant(ctx context.Context, check appGrantCheck) (alreadyGone bool, err error) {
	logDecision := func(decision string, reason string, extra ...any) {
		fields := append([]any{
			"mode", service.appGrantMode.String(),
			"route", check.route,
			"app_id", check.appID,
			"person_hash", check.personHash,
			"decision", decision,
			"reason", reason,
		}, extra...)
		switch {
		case decision == grantDecisionAllow || decision == grantDecisionAlreadyGone:
			service.logger.Info("app grant check", fields...)
		case decision == grantDecisionLookupFailed && service.appGrantMode != enforcement.LogOnly:
			// Enforce fails the request with a 500, so this is an error.
			service.logger.Error("app grant check", fields...)
		default:
			service.logger.Warn("app grant check", fields...)
		}
	}

	user, lookupErr := service.client.GetUser(ctx, check.ntfyUserID)
	switch {
	case lookupErr == nil && userHasAppGrant(user, check.appID):
		logDecision(grantDecisionAllow, grantReasonGrantHeld)
		return false, nil
	case lookupErr == nil:
		if service.appGrantMode == enforcement.LogOnly {
			logDecision(grantDecisionWouldReject, grantReasonNoGrant)
			return false, nil
		}
		logDecision(grantDecisionRejected, grantReasonNoGrant)
		return false, ErrPersonNotInApp
	case errors.Is(lookupErr, ntfycli.ErrNotFound):
		if check.route == grantRouteDeprovision {
			logDecision(grantDecisionAlreadyGone, grantReasonUserMissing)
			return true, nil
		}
		logDecision(grantDecisionRejected, grantReasonUserMissing)
		return false, ErrPersonNotInApp
	default:
		logDecision(grantDecisionLookupFailed, grantReasonLookupError, "error", lookupErr)
		if service.appGrantMode == enforcement.LogOnly {
			return false, nil
		}
		return false, lookupErr
	}
}
