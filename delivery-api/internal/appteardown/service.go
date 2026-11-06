// Package appteardown removes an app and everything it owns in a safe, repeatable order.
package appteardown

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
)

// teardownTimeout bounds the phases that run after the upstream call (unbind, publisher removal and
// object delete together). They run on a context detached from the request so a client disconnect cannot
// stop the cascade partway.
const teardownTimeout = 30 * time.Second

// Failure classes. Each wraps the cause in its message for the server log; none reaches a client.
var (
	// ErrInvalidAppID means the app id is malformed; no upstream call was made.
	ErrInvalidAppID = errors.New("invalid app_id")
	// ErrUpstream means provisioning-api failed or answered non-2xx; delivery data is untouched.
	ErrUpstream = errors.New("provisioning-api teardown failed")
	// ErrBusy means a mint holds the per-app lock; the binding is already gone, retry the call.
	ErrBusy = errors.New("publisher mint already in progress")
	// ErrUnavailable means a store step failed after the upstream call; retry the call.
	ErrUnavailable = errors.New("app teardown unavailable")
)

// Provisioner is the provisioning-api teardown call. *provisionclient.Client satisfies it.
type Provisioner interface {
	DeprovisionApp(ctx context.Context, appID string) (provisionclient.Passthrough, error)
}

// Binder removes an app's binding and keys. *appidentity.Service satisfies it.
type Binder interface {
	Unbind(ctx context.Context, appID string) (bool, error)
}

// PublisherRemover deletes the stored publisher under the per-app mint lock. *publisher.Service satisfies it.
type PublisherRemover interface {
	RemoveExclusive(ctx context.Context, appID string) error
}

// ObjectStore deletes the checks, routes and destinations an app owns. *PGStore satisfies it.
type ObjectStore interface {
	DeleteOwnedObjects(ctx context.Context, appID string) (Deleted, error)
}

// Result is what a completed cascade reports.
type Result struct {
	AppID   string  `json:"app_id"`
	Removed bool    `json:"removed"`
	Unbound bool    `json:"unbound"`
	Deleted Deleted `json:"deleted"`
}

// ServiceConfig carries the Service dependencies; every field is required.
type ServiceConfig struct {
	Provisioner Provisioner
	Binder      Binder
	Publisher   PublisherRemover
	Store       ObjectStore
	Logger      *slog.Logger
}

// Service runs the deprovision-app cascade.
type Service struct {
	provisioner Provisioner
	binder      Binder
	publisher   PublisherRemover
	store       ObjectStore
	logger      *slog.Logger
}

// NewService builds a Service. A nil logger uses slog.Default().
func NewService(config ServiceConfig) *Service {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		provisioner: config.Provisioner, binder: config.Binder, publisher: config.Publisher,
		store: config.Store, logger: logger,
	}
}

// Deprovision removes appID and everything it owns: provisioning-api's ntfy users, then the binding and
// keys, the stored publisher, and the checks, routes and destinations the app owns. Every step is
// idempotent, so a failed run is finished by repeating the call. Order:
//
//  1. provisioning-api deprovision-app on ctx; any failure or non-2xx aborts with delivery data intact.
//  2. Unbind (no binding is not an error: Result.Unbound is false).
//  3. RemoveExclusive under the per-app mint lock (a mint in flight is ErrBusy).
//  4. DeleteOwnedObjects (one transaction).
//
// Accepted window: while step 1 runs the app's dak_ keys are still valid, so the app could re-provision a
// person or re-mint and leave an ntfy user the sweep will not see again. Unbind is deliberately not moved
// first, because an upstream failure must leave the binding, publisher and data in place. The operator
// repeats the call to clear anything created in that window.
func (service *Service) Deprovision(ctx context.Context, appID string) (Result, error) {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return Result{}, ErrInvalidAppID
	}

	// Fail closed: a missing dependency must not nil-panic mid-cascade.
	if service.provisioner == nil || service.binder == nil || service.publisher == nil || service.store == nil {
		service.logger.Error("app teardown unavailable: missing dependency", "app_id", appID)
		return Result{}, fmt.Errorf("%w: missing dependency", ErrUnavailable)
	}

	started := time.Now()
	answer, upstreamErr := service.provisioner.DeprovisionApp(ctx, appID)
	if upstreamErr != nil {
		service.logger.Error("app teardown phase failed",
			"app_id", appID, "phase", "upstream", "rc", "error", "duration", time.Since(started), "error", upstreamErr)
		return Result{}, fmt.Errorf("%w: %v", ErrUpstream, upstreamErr)
	}
	if answer.Status < 200 || answer.Status > 299 {
		service.logger.Error("app teardown phase failed",
			"app_id", appID, "phase", "upstream", "duration", time.Since(started), "rc", answer.Status)
		return Result{}, fmt.Errorf("%w: status %d", ErrUpstream, answer.Status)
	}
	service.logger.Info("app teardown phase done",
		"app_id", appID, "phase", "upstream", "rc", answer.Status, "duration", time.Since(started))

	// The remaining phases must not be cut short by a client disconnect.
	rest, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
	defer cancel()

	started = time.Now()
	unbound, unbindErr := service.binder.Unbind(rest, appID)
	if unbindErr != nil {
		service.logger.Error("app teardown phase failed",
			"app_id", appID, "phase", "unbind", "rc", "error", "duration", time.Since(started), "error", unbindErr)
		return Result{}, fmt.Errorf("%w: unbind: %v", ErrUnavailable, unbindErr)
	}
	service.logger.Info("app teardown phase done",
		"app_id", appID, "phase", "unbind", "unbound", unbound, "duration", time.Since(started))

	started = time.Now()
	if removeErr := service.publisher.RemoveExclusive(rest, appID); removeErr != nil {
		service.logger.Error("app teardown phase failed",
			"app_id", appID, "phase", "publisher", "rc", "error", "duration", time.Since(started), "error", removeErr)
		if errors.Is(removeErr, publisher.ErrMintBusy) {
			return Result{}, fmt.Errorf("%w: %v", ErrBusy, removeErr)
		}
		return Result{}, fmt.Errorf("%w: publisher: %v", ErrUnavailable, removeErr)
	}
	service.logger.Info("app teardown phase done",
		"app_id", appID, "phase", "publisher", "duration", time.Since(started))

	started = time.Now()
	deleted, deleteErr := service.store.DeleteOwnedObjects(rest, appID)
	if deleteErr != nil {
		service.logger.Error("app teardown phase failed",
			"app_id", appID, "phase", "objects", "rc", "error", "duration", time.Since(started), "error", deleteErr)
		return Result{}, fmt.Errorf("%w: objects: %v", ErrUnavailable, deleteErr)
	}
	service.logger.Info("app teardown phase done",
		"app_id", appID, "phase", "objects", "duration", time.Since(started),
		"checks", deleted.Checks, "routes", deleted.Routes, "destinations", deleted.Destinations,
		"orphaned_routes", deleted.OrphanedRoutes, "orphaned_checks", deleted.OrphanedChecks,
		"held_messages", deleted.HeldMessages)

	return Result{AppID: appID, Removed: true, Unbound: unbound, Deleted: deleted}, nil
}
