package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	// DefaultProberInterval is how often the prober scans for due probe checks after its immediate
	// first scan.
	DefaultProberInterval = 10 * time.Second
	// DefaultProberConcurrency bounds the probes in flight at once. The pgx pool is shared and a probe
	// holds no connection during its request, only during its short result write.
	DefaultProberConcurrency = 4
	// ProbeResultWriteTimeout bounds one result write.
	ProbeResultWriteTimeout = 5 * time.Second
	// MaxProberShutdown is the longest an in-flight probe and its result write can run after
	// shutdown starts: the largest probe timeout plus the result write bound.
	MaxProberShutdown = MaxProbeTimeoutSeconds*time.Second + ProbeResultWriteTimeout
)

const probeReasonOK = "ok"

// errProberScanPanicked is what Scan returns after recovering a panic.
const errProberScanPanicked proberError = "heartbeat prober: scan panicked"

// proberError is a constant error string.
type proberError string

func (err proberError) Error() string { return string(err) }

// ProbeStore is the part of the store the prober needs. *PGStore satisfies it.
type ProbeStore interface {
	ClaimDueProbes(ctx context.Context, now time.Time, limit int) ([]ProbeClaim, error)
	RecordProbeResult(ctx context.Context, checkID int64, configVersion int64, result ProbeResult, now, holdOffUntil time.Time) (StoredCheck, bool, error)
}

// ProberConfig configures a Prober. Store, HoldOffUntil, Client and Freshness are required, as are
// a positive Interval and Concurrency; anything else fails closed. Now defaults to time.Now and
// Logger to slog.Default(). A nil Sealer is valid: a probe with a sealed token then records a config
// failure.
type ProberConfig struct {
	Store        ProbeStore
	HoldOffUntil func() time.Time
	Client       *http.Client
	Sealer       *seal.Sealer
	Interval     time.Duration
	Concurrency  int
	Now          func() time.Time
	Logger       *slog.Logger
	// Freshness receives Now() after every scan pass that claimed successfully.
	Freshness *Freshness
}

// errInvalidProberConfig means a required ProberConfig field is missing or out of range.
const errInvalidProberConfig proberError = "heartbeat prober: invalid configuration"

// Prober claims due probe checks and runs them with bounded concurrency.
type Prober struct {
	config       ProberConfig
	writeTimeout time.Duration
	inFlight     atomic.Int64
	probes       sync.WaitGroup
}

// NewProber validates config and returns a Prober. It returns an error for a nil Store,
// HoldOffUntil, Client or Freshness, an Interval that is not positive or a Concurrency below 1.
func NewProber(config ProberConfig) (*Prober, error) {
	if config.Store == nil || config.HoldOffUntil == nil || config.Client == nil || config.Freshness == nil ||
		config.Interval <= 0 || config.Concurrency < 1 {
		return nil, errInvalidProberConfig
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Prober{config: config, writeTimeout: ProbeResultWriteTimeout}, nil
}

// Scan runs one pass: it claims at most the free concurrency slots, starts each claimed probe
// detached from ctx and records freshness, then returns without waiting for the probes. A claim error
// is logged (a context cancellation is not), returned and does not record freshness; a panic is
// recovered, logged and returned the same way. Scan is not safe to call concurrently with itself or
// with Wait.
func (prober *Prober) Scan(ctx context.Context) (scanErr error) {
	logger := prober.config.Logger
	defer func() {
		if recover() != nil {
			logger.Error("heartbeat probe scan panicked")
			scanErr = errProberScanPanicked
		}
	}()

	var claims []ProbeClaim
	free := prober.config.Concurrency - int(prober.inFlight.Load())
	if free > 0 {
		claimed, claimErr := prober.config.Store.ClaimDueProbes(ctx, prober.config.Now(), free)
		if claimErr != nil {
			if !errors.Is(claimErr, context.Canceled) {
				logger.Error("heartbeat probe scan failed", "error", claimErr)
			}
			return claimErr
		}
		claims = claimed
	}
	// Recorded before the detached probes start, so it precedes any of their results.
	prober.config.Freshness.Record(prober.config.Now())
	for _, claim := range claims {
		prober.inFlight.Add(1)
		prober.probes.Add(1)
		go prober.runClaim(ctx, claim)
	}
	return nil
}

// Wait blocks until every started probe and its result write are done.
func (prober *Prober) Wait() {
	prober.probes.Wait()
}

// runClaim probes one claim and writes the result. It holds a concurrency slot until done.
func (prober *Prober) runClaim(ctx context.Context, claim ProbeClaim) {
	logger := prober.config.Logger
	defer prober.probes.Done()
	defer prober.inFlight.Add(-1)
	defer func() {
		if recover() != nil {
			logger.Error("heartbeat probe panicked", "check_id", claim.CheckID)
		}
	}()

	logger.Debug("heartbeat probe started",
		"check_id", claim.CheckID, "app_id", claim.AppID, "check", claim.Name,
		"timeout_seconds", claim.Config.TimeoutSeconds)
	started := time.Now()
	result := runProbe(ctx, prober.config.Client, prober.config.Sealer, claim)
	duration := time.Since(started)

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), prober.writeTimeout)
	defer cancel()
	_, applied, writeErr := prober.config.Store.RecordProbeResult(
		writeCtx, claim.CheckID, claim.ConfigVersion, result, prober.config.Now(), prober.config.HoldOffUntil())
	if writeErr != nil {
		logger.Error("heartbeat probe result write failed", "check_id", claim.CheckID, "error", writeErr)
		return
	}

	reason := probeReasonOK
	if result.Reason != nil {
		reason = *result.Reason
	}
	attrs := []any{
		"check_id", claim.CheckID, "app_id", claim.AppID, "check", claim.Name, "reason", reason,
	}
	if result.Status != nil {
		attrs = append(attrs, "status", *result.Status)
	}
	attrs = append(attrs, "duration_ms", duration.Milliseconds(), "applied", applied)
	logger.Info("heartbeat probe finished", attrs...)
	if !applied {
		logger.Info("heartbeat probe result discarded", "check_id", claim.CheckID)
	}
}

// RunProber scans immediately and then every config.Interval until ctx is done, then waits for the
// in-flight probes and their result writes before returning. Probes and result writes run on
// contexts that ctx's cancellation does not cancel, so shutdown finishes within MaxProberShutdown.
// Invalid configuration logs an error and returns at once.
func RunProber(ctx context.Context, config ProberConfig) {
	prober, newErr := NewProber(config)
	if newErr != nil {
		logger := config.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Error("heartbeat prober not started: invalid configuration")
		return
	}
	defer prober.Wait()

	if ctx.Err() != nil {
		return
	}
	_ = prober.Scan(ctx)
	ticker := time.NewTicker(prober.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			_ = prober.Scan(ctx)
		}
	}
}
