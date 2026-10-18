//go:build integration

package registry_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	rotateWebhookURL    = "https://hooks.example.com/in/secret-path-xyz"
	rotatePatchedURL    = "https://hooks.example.com/in/patched-path"
	rotatePatchedExpiry = "2031-01-01T00:00:00Z"
	rotateParallelCount = 8
	// blockedPatchWait is how long the PATCH is given to (wrongly) complete while
	// the rotation holds the row lock; only the negative assertion uses it.
	blockedPatchWait = 300 * time.Millisecond
	rotateLockQuery  = "SELECT 1 FROM destinations WHERE name = $1 FOR UPDATE NOWAIT"
)

var rotateInitialSecret = strings.Repeat("s", 40)

// hookStore wraps a Store so a test can run code inside the next
// UpdateDestination mutate step, while the row lock is still held.
type hookStore struct {
	registry.Store
	mutex sync.Mutex
	hook  func()
}

func (store *hookStore) setHook(hook func()) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.hook = hook
}

func (store *hookStore) takeHook() func() {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	hook := store.hook
	store.hook = nil
	return hook
}

func (store *hookStore) UpdateDestination(
	ctx context.Context, name string, mutate func(registry.StoredDestination) (registry.StoredDestination, error),
) (registry.StoredDestination, error) {
	hook := store.takeHook()
	return store.Store.UpdateDestination(ctx, name,
		func(current registry.StoredDestination) (registry.StoredDestination, error) {
			mutated, mutateErr := mutate(current)
			if mutateErr == nil && hook != nil {
				hook()
			}
			return mutated, mutateErr
		})
}

// newRotateService returns a Service over a real Postgres store (wrapped in a
// hookStore) holding one webhook destination named "hook".
func newRotateService(testInstance *testing.T) (context.Context, *registry.Service, *hookStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, pgStore, pool := newTestStore(testInstance)
	sealer, sealErr := seal.New(bytes.Repeat([]byte{7}, seal.KeySize))
	if sealErr != nil {
		testInstance.Fatalf("seal.New: %v", sealErr)
	}
	store := &hookStore{Store: pgStore}
	service := registry.NewService(store, sealer, nil)
	if _, err := service.CreateDestination(ctx, registry.DestinationInput{
		Name: "hook", Type: registry.TypeWebhook,
		Secrets: map[string]string{"url": rotateWebhookURL, "signing_secret": rotateInitialSecret},
	}); err != nil {
		testInstance.Fatalf("create webhook: %v", err)
	}
	return ctx, service, store, pool
}

// resolvedWebhook returns the plaintext secrets and config of the webhook.
func resolvedWebhook(
	ctx context.Context, testInstance *testing.T, service *registry.Service,
) registry.ResolvedDestination {
	testInstance.Helper()
	resolved, err := service.Resolve(ctx, "", "hook", "")
	if err != nil || len(resolved) != 1 {
		testInstance.Fatalf("resolve hook: %v (%d results)", err, len(resolved))
	}
	return resolved[0]
}

// TestRotationMutateHoldsTheRowLock is the lock proof, part one: a real
// rotation must run its mutate step under the destination's row lock. It
// fails if the rotation ran outside the store's row-locked update, because the
// hook (which probes the lock from another connection) would then never see
// the lock held, or never run at all.
func TestRotationMutateHoldsTheRowLock(testInstance *testing.T) {
	ctx, service, store, pool := newRotateService(testInstance)
	hookRan := false
	store.setHook(func() {
		hookRan = true
		expectLockNotAvailable(ctx, testInstance, pool, rotateLockQuery, "hook")
	})

	if _, _, err := service.RotateWebhookSecret(ctx, "hook", 24); err != nil {
		testInstance.Fatalf("rotate: %v", err)
	}

	if !hookRan {
		testInstance.Fatal("the rotation never ran inside the store's UpdateDestination mutate step")
	}
}

// TestRotationBlocksAConcurrentPatchUntilItCommits is the lock proof, part two:
// a PATCH launched while a rotation is held inside its mutate step must not
// complete until the rotation commits, and both effects must survive.
func TestRotationBlocksAConcurrentPatchUntilItCommits(testInstance *testing.T) {
	ctx, service, store, _ := newRotateService(testInstance)
	inMutate := make(chan struct{})
	release := make(chan struct{})
	store.setHook(func() {
		close(inMutate)
		<-release
	})

	type rotation struct {
		secret string
		err    error
	}
	rotationDone := make(chan rotation, 1)
	go func() {
		_, secret, err := service.RotateWebhookSecret(ctx, "hook", 24)
		rotationDone <- rotation{secret: secret, err: err}
	}()
	<-inMutate

	patchDone := make(chan error, 1)
	go func() {
		_, err := service.UpdateDestination(ctx, "hook", registry.DestinationPatch{
			Config:  map[string]string{"previous_secret_expires_at": rotatePatchedExpiry},
			Secrets: map[string]string{"url": rotatePatchedURL},
		})
		patchDone <- err
	}()

	select {
	case err := <-patchDone:
		testInstance.Fatalf("the PATCH completed (err = %v) while the rotation still held the row lock", err)
	case <-time.After(blockedPatchWait):
	}

	close(release)
	rotated := <-rotationDone
	if rotated.err != nil {
		testInstance.Fatalf("rotate: %v", rotated.err)
	}
	if patchErr := <-patchDone; patchErr != nil {
		testInstance.Fatalf("patch: %v", patchErr)
	}

	final := resolvedWebhook(ctx, testInstance, service)
	if final.Secrets["signing_secret"] != rotated.secret ||
		final.Secrets["signing_secret_previous"] != rotateInitialSecret {
		testInstance.Fatalf("the rotation was lost or corrupted: %v", final.Secrets)
	}
	if final.Secrets["url"] != rotatePatchedURL || final.Config["previous_secret_expires_at"] != rotatePatchedExpiry {
		testInstance.Fatalf("the patch was lost: secrets %v config %v", final.Secrets, final.Config)
	}
}

// TestParallelRotationsSmoke is an extra smoke check, not the locking proof:
// N parallel rotations all succeed, issue N distinct secrets, and leave the
// last two links of the chain consistent.
func TestParallelRotationsSmoke(testInstance *testing.T) {
	ctx, service, _, _ := newRotateService(testInstance)
	issued := make(chan string, rotateParallelCount)
	errs := make(chan error, rotateParallelCount)
	var group sync.WaitGroup
	for range rotateParallelCount {
		group.Add(1)
		go func() {
			defer group.Done()
			_, secret, err := service.RotateWebhookSecret(ctx, "hook", 24)
			if err != nil {
				errs <- err
				return
			}
			issued <- secret
		}()
	}
	group.Wait()
	close(issued)
	close(errs)

	for err := range errs {
		testInstance.Fatalf("parallel rotate: %v", err)
	}
	distinct := map[string]bool{rotateInitialSecret: true}
	for secret := range issued {
		distinct[secret] = true
	}
	if len(distinct) != rotateParallelCount+1 {
		testInstance.Fatalf("distinct secrets = %d, want %d", len(distinct), rotateParallelCount+1)
	}
	final := resolvedWebhook(ctx, testInstance, service)
	if !distinct[final.Secrets["signing_secret"]] || !distinct[final.Secrets["signing_secret_previous"]] ||
		final.Secrets["signing_secret"] == final.Secrets["signing_secret_previous"] {
		testInstance.Fatalf("inconsistent final secrets: %v", final.Secrets)
	}
}
