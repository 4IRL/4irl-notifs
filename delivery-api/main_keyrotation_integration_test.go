//go:build integration

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/keyrotation"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	exitContractBudget = 60 * time.Second
	exitOldKeyHex      = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	exitNewKeyHex      = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

func exitContractSealers(testInstance *testing.T) (oldSealer *seal.Sealer, dualSealer *seal.Sealer) {
	testInstance.Helper()
	oldKey, oldKeyErr := seal.ParseHexKey(exitOldKeyHex)
	newKey, newKeyErr := seal.ParseHexKey(exitNewKeyHex)
	if joinErr := errors.Join(oldKeyErr, newKeyErr); joinErr != nil {
		testInstance.Fatalf("parse keys: %v", joinErr)
	}
	oldSealer, oldErr := seal.New(oldKey)
	dualSealer, dualErr := seal.NewWithPrevious(newKey, oldKey)
	if joinErr := errors.Join(oldErr, dualErr); joinErr != nil {
		testInstance.Fatalf("build sealers: %v", joinErr)
	}
	return oldSealer, dualSealer
}

func seedExitContractDestinations(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, sealer *seal.Sealer, badRow bool) {
	testInstance.Helper()
	for _, name := range []string{"dest_a", "dest_b"} {
		blob, sealErr := sealer.Seal([]byte("secret-"+name), registry.DestinationSecretsAAD(name))
		if sealErr != nil {
			testInstance.Fatalf("seal: %v", sealErr)
		}
		if _, execErr := pool.Exec(ctx, `INSERT INTO destinations (name, type, sealed_secrets) VALUES ($1, 'webhook', $2)`, name, blob); execErr != nil {
			testInstance.Fatalf("insert destination: %v", execErr)
		}
	}
	if badRow {
		if _, execErr := pool.Exec(ctx, `UPDATE destinations SET sealed_secrets = $1 WHERE name = 'dest_b'`, []byte{0x01}); execErr != nil {
			testInstance.Fatalf("corrupt destination: %v", execErr)
		}
	}
}

// TestRotateKeysExitContract pins the exit status the runbook depends on:
// rotate-keys returns an error wrapping ErrIncomplete when a row needs attention
// and nil on a clean database.
func TestRotateKeysExitContract(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), exitContractBudget)
	defer cancel()
	oldSealer, dualSealer := exitContractSealers(testInstance)

	badPool := pgtest.IsolatedPool(ctx, testInstance)
	seedExitContractDestinations(ctx, testInstance, badPool, oldSealer, true)
	if rotateErr := rotateKeysOn(ctx, newTestLogger(), badPool, dualSealer); !errors.Is(rotateErr, keyrotation.ErrIncomplete) {
		testInstance.Fatalf("rotate-keys with a bad row: err = %v, want ErrIncomplete", rotateErr)
	}

	cleanPool := pgtest.IsolatedPool(ctx, testInstance)
	seedExitContractDestinations(ctx, testInstance, cleanPool, oldSealer, false)
	if rotateErr := rotateKeysOn(ctx, newTestLogger(), cleanPool, dualSealer); rotateErr != nil {
		testInstance.Fatalf("rotate-keys on a clean database: %v", rotateErr)
	}
}

// TestVerifyKeysExitContract pins that verify-keys fails when a row does not open
// under the primary key alone and succeeds on a clean, fully rotated database.
func TestVerifyKeysExitContract(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), exitContractBudget)
	defer cancel()
	oldSealer, dualSealer := exitContractSealers(testInstance)

	// Rows still under the old key fail verification.
	staleErr := func() error {
		pool := pgtest.IsolatedPool(ctx, testInstance)
		seedExitContractDestinations(ctx, testInstance, pool, oldSealer, false)
		return verifyKeysOn(ctx, newTestLogger(), pool, dualSealer)
	}()
	if !errors.Is(staleErr, keyrotation.ErrIncomplete) {
		testInstance.Fatalf("verify-keys with old-key rows: err = %v, want ErrIncomplete", staleErr)
	}

	cleanPool := pgtest.IsolatedPool(ctx, testInstance)
	seedExitContractDestinations(ctx, testInstance, cleanPool, oldSealer, false)
	if rotateErr := rotateKeysOn(ctx, newTestLogger(), cleanPool, dualSealer); rotateErr != nil {
		testInstance.Fatalf("rotate-keys: %v", rotateErr)
	}
	if verifyErr := verifyKeysOn(ctx, newTestLogger(), cleanPool, dualSealer); verifyErr != nil {
		testInstance.Fatalf("verify-keys on a rotated database: %v", verifyErr)
	}
}
