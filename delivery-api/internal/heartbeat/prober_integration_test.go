//go:build integration

package heartbeat_test

// End-to-end test of the prober on real Postgres and the real alert path: the real Prober claims due
// probe checks, requests an httptest HTTPS target through the guarded transport client, writes the
// result through PGStore and the real Service flushes the alerts to the webhook receiver. The
// stack's shifted clock is the prober's time base, so passes run without waiting out the interval.
// Scan then Wait is the deterministic seam: no pass depends on a timer.

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const proberPassStep = heartbeat.MinProbeIntervalSeconds * time.Second

// probeTarget is the stand-in website: its status is switchable and a gate can hold requests open.
type probeTarget struct {
	server   *httptest.Server
	status   atomic.Int32
	requests atomic.Int64
	// arrived receives one signal per request that reached the handler.
	arrived chan struct{}
	mutex   sync.Mutex
	gate    chan struct{}
}

func newProbeTarget(testInstance *testing.T) *probeTarget {
	testInstance.Helper()
	target := &probeTarget{arrived: make(chan struct{}, 64)}
	target.status.Store(http.StatusOK)
	target.server = httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		target.requests.Add(1)
		select {
		case target.arrived <- struct{}{}:
		default:
		}
		target.mutex.Lock()
		gate := target.gate
		target.mutex.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-request.Context().Done():
				return
			}
		}
		responseWriter.WriteHeader(int(target.status.Load()))
	}))
	testInstance.Cleanup(func() {
		target.release()
		target.server.Close()
	})
	return target
}

// block makes every following request wait until release.
func (target *probeTarget) block() {
	target.mutex.Lock()
	defer target.mutex.Unlock()
	target.gate = make(chan struct{})
	// Drop arrival signals from earlier requests so the next waitArrival sees a held one.
	for {
		select {
		case <-target.arrived:
		default:
			return
		}
	}
}

// release lets the held requests answer with the current status.
func (target *probeTarget) release() {
	target.mutex.Lock()
	defer target.mutex.Unlock()
	if target.gate != nil {
		close(target.gate)
		target.gate = nil
	}
}

// waitArrival blocks until a request reached the handler since the last wait.
func (target *probeTarget) waitArrival(testInstance *testing.T) {
	testInstance.Helper()
	select {
	case <-target.arrived:
	case <-time.After(e2eWaitTimeout):
		testInstance.Fatalf("no request reached the probe target within %s", e2eWaitTimeout)
	}
}

// proberHarness is the heartbeat stack plus a probe target and a Prober on the stack's clock.
type proberHarness struct {
	*heartbeatStack
	target *probeTarget
	prober *heartbeat.Prober
}

func newProberHarness(testInstance *testing.T) *proberHarness {
	testInstance.Helper()
	stack := startHeartbeatStack(testInstance)
	target := newProbeTarget(testInstance)

	roots := x509.NewCertPool()
	roots.AddCert(target.server.Certificate())
	client := transport.New(transport.Config{
		AllowAddr: func(netip.Addr) bool { return true },
		TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	})
	prober, newErr := heartbeat.NewProber(heartbeat.ProberConfig{
		Store:        stack.store,
		HoldOffUntil: stack.service.HoldOffUntil,
		Client:       client,
		Sealer:       stack.sealer,
		Interval:     time.Minute,
		Concurrency:  heartbeat.DefaultProberConcurrency,
		Now:          stack.clock.Now,
		Logger:       slog.New(slog.DiscardHandler),
		Freshness:    heartbeat.NewFreshness(stack.clock.Now),
	})
	if newErr != nil {
		testInstance.Fatalf("NewProber: %v", newErr)
	}
	// Registered last so (LIFO) it runs first: no probe outlives the test or its pool.
	testInstance.Cleanup(func() {
		target.release()
		prober.Wait()
	})
	return &proberHarness{heartbeatStack: stack, target: target, prober: prober}
}

// create stores a probe check at the target through the store (the service validator rejects the
// IP-literal host; the DB CHECK only needs https://).
func (harness *proberHarness) create(testInstance *testing.T, name string, token *heartbeat.AccessToken) heartbeat.StoredCheck {
	testInstance.Helper()
	created, createErr := harness.store.Create(harness.ctx, heartbeat.NewCheck{
		AppID: e2eAppID, Name: name, PeriodSeconds: 7, GraceSeconds: 0,
		DestinationNames: []string{e2eFirstHook, e2eSecondHook},
		Probe: &heartbeat.ProbeConfig{
			URL: harness.target.server.URL + "/health", ExpectedStatuses: []int{http.StatusOK},
			TimeoutSeconds: 5, IntervalSeconds: heartbeat.MinProbeIntervalSeconds,
		},
		AccessToken: token,
	}, harness.clock.Now())
	if createErr != nil {
		testInstance.Fatalf("create probe check %s: %v", name, createErr)
	}
	return created
}

// pastHoldOff moves the clock past the startup hold-off (and past the first due time).
func (harness *proberHarness) pastHoldOff() {
	harness.clock.Advance(heartbeat.StartupHoldOff + time.Second)
}

func (harness *proberHarness) scan(testInstance *testing.T) {
	testInstance.Helper()
	if scanErr := harness.prober.Scan(harness.ctx); scanErr != nil {
		testInstance.Fatalf("Scan: %v", scanErr)
	}
}

// pass advances the clock one probe interval, scans and waits for every result write.
func (harness *proberHarness) pass(testInstance *testing.T) {
	testInstance.Helper()
	harness.clock.Advance(proberPassStep)
	harness.scan(testInstance)
	harness.prober.Wait()
}

// makeDue sets the check's next_probe_at to the clock's now. A claim advances next_probe_at by the
// interval, so a case that must not move the clock makes the check due this way (test coupling to
// that column only).
func (harness *proberHarness) makeDue(testInstance *testing.T, checkID int64) {
	testInstance.Helper()
	if _, execErr := harness.pool.Exec(harness.ctx,
		`UPDATE heartbeat_check_probes SET next_probe_at = $1 WHERE check_id = $2`,
		harness.clock.Now(), checkID); execErr != nil {
		testInstance.Fatalf("make probe due: %v", execErr)
	}
}

func (harness *proberHarness) get(testInstance *testing.T, checkID int64) heartbeat.StoredCheck {
	testInstance.Helper()
	check, getErr := harness.store.Get(harness.ctx, checkID)
	if getErr != nil {
		testInstance.Fatalf("Get %d: %v", checkID, getErr)
	}
	return check
}

// requireProbeState asserts the check's state and consecutive failure counter.
func (harness *proberHarness) requireProbeState(testInstance *testing.T, checkID int64, wantState string, wantFailures int) heartbeat.StoredCheck {
	testInstance.Helper()
	check := harness.get(testInstance, checkID)
	if check.State != wantState || check.Probe.ConsecutiveFailures != wantFailures {
		testInstance.Fatalf("probe state = %q with %d consecutive failures, want %q with %d",
			check.State, check.Probe.ConsecutiveFailures, wantState, wantFailures)
	}
	return check
}

func requireLastResultReason(testInstance *testing.T, check heartbeat.StoredCheck, want string) {
	testInstance.Helper()
	last := check.Probe.LastResult
	if last == nil || last.Reason == nil || *last.Reason != want {
		testInstance.Fatalf("last result = %+v, want reason %q", last, want)
	}
}

// failPasses runs count passes against a target answering 500.
func (harness *proberHarness) failPasses(testInstance *testing.T, count int) {
	testInstance.Helper()
	harness.target.status.Store(http.StatusInternalServerError)
	for range count {
		harness.pass(testInstance)
	}
}

// succeedPass runs one pass against a target answering 200.
func (harness *proberHarness) succeedPass(testInstance *testing.T) {
	testInstance.Helper()
	harness.target.status.Store(http.StatusOK)
	harness.pass(testInstance)
}

// webhookCounts counts the received webhook calls by "<payload type>|<check name>".
func webhookCounts(testInstance *testing.T, requests []hookRequest) map[string]int {
	testInstance.Helper()
	counts := map[string]int{}
	for _, request := range requests {
		var body struct {
			Payload struct {
				Type  string `json:"type"`
				Check string `json:"check"`
			} `json:"payload"`
		}
		if decodeErr := json.Unmarshal(request.body, &body); decodeErr != nil {
			testInstance.Fatalf("decode webhook body %q: %v", request.body, decodeErr)
		}
		counts[body.Payload.Type+"|"+body.Payload.Check]++
	}
	return counts
}

func TestProberNewProbeGoesUpSilentlyOnItsFirstSuccess(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()

	harness.succeedPass(testInstance)

	up := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateUp, 0)
	if up.LastPingAt == nil || up.Probe.LastResult == nil || up.Probe.LastResult.Reason != nil ||
		up.Probe.LastResult.Status == nil || *up.Probe.LastResult.Status != http.StatusOK {
		testInstance.Fatalf("after a success: last ping %v, last result %+v, want a ping and a 200 success", up.LastPingAt, up.Probe.LastResult)
	}
	if got := harness.target.requests.Load(); got != 1 {
		testInstance.Fatalf("target saw %d requests, want 1", got)
	}
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, 0)
}

func TestProberThresholdTakesAnUpCheckDownOnceAndStaysQuiet(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()
	harness.succeedPass(testInstance)

	harness.failPasses(testInstance, heartbeat.ProbeFailureThreshold-1)
	below := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateUp, heartbeat.ProbeFailureThreshold-1)
	requireLastResultReason(testInstance, below, heartbeat.ReasonHTTPStatus)
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, 0)

	harness.pass(testInstance)
	down := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateDown, heartbeat.ProbeFailureThreshold)
	if down.DownReason == nil || *down.DownReason != heartbeat.ReasonHTTPStatus {
		testInstance.Fatalf("down reason = %v, want %q", down.DownReason, heartbeat.ReasonHTTPStatus)
	}
	harness.tick(testInstance)
	harness.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, harness.receiver.received(), 0, "heartbeat.down", heartbeat.ReasonHTTPStatus)

	// Further failures keep it down with the same reason and send nothing more.
	harness.failPasses(testInstance, 2)
	still := harness.get(testInstance, probe.ID)
	if still.State != heartbeat.StateDown || still.DownReason == nil || *still.DownReason != heartbeat.ReasonHTTPStatus {
		testInstance.Fatalf("after further failures: state %q reason %v, want down as %q", still.State, still.DownReason, heartbeat.ReasonHTTPStatus)
	}
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, 2)
}

func TestProberSuccessResetsTheFailureCounter(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()
	harness.succeedPass(testInstance)

	harness.failPasses(testInstance, heartbeat.ProbeFailureThreshold-1)
	harness.requireProbeState(testInstance, probe.ID, heartbeat.StateUp, heartbeat.ProbeFailureThreshold-1)
	harness.succeedPass(testInstance)
	harness.requireProbeState(testInstance, probe.ID, heartbeat.StateUp, 0)

	harness.failPasses(testInstance, heartbeat.ProbeFailureThreshold-1)
	harness.requireProbeState(testInstance, probe.ID, heartbeat.StateUp, heartbeat.ProbeFailureThreshold-1)
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, 0)
}

func TestProberNewCheckWithNFailuresGoesDownWithOneAlert(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()

	harness.failPasses(testInstance, heartbeat.ProbeFailureThreshold)

	down := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateDown, heartbeat.ProbeFailureThreshold)
	if down.DownReason == nil || *down.DownReason != heartbeat.ReasonHTTPStatus {
		testInstance.Fatalf("down reason = %v, want %q", down.DownReason, heartbeat.ReasonHTTPStatus)
	}
	harness.tick(testInstance)
	harness.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, harness.receiver.received(), 0, "heartbeat.down", heartbeat.ReasonHTTPStatus)
	harness.requireQuiet(testInstance, 2)
}

func TestProberHoldOffDefersTheTransitionWithoutLosingTheCount(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.target.status.Store(http.StatusInternalServerError)

	// The clock stays inside the real hold-off. Each claim moves next_probe_at past it, so the check
	// is made due before every pass.
	for range heartbeat.ProbeFailureThreshold {
		harness.makeDue(testInstance, probe.ID)
		harness.scan(testInstance)
		harness.prober.Wait()
	}
	if harness.clock.Now().After(harness.service.HoldOffUntil()) {
		testInstance.Fatalf("the clock left the startup hold-off during the deferral phase")
	}
	deferred := harness.requireProbeState(testInstance, probe.ID, probe.State, heartbeat.ProbeFailureThreshold)
	requireLastResultReason(testInstance, deferred, heartbeat.ReasonHTTPStatus)
	if deferred.PendingAlert != nil || deferred.DownSince != nil {
		testInstance.Fatalf("deferred check has pending alert %v and down since %v, want neither", deferred.PendingAlert, deferred.DownSince)
	}
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, 0)
	if got := harness.target.requests.Load(); got != heartbeat.ProbeFailureThreshold {
		testInstance.Fatalf("target saw %d requests during the deferral, want %d", got, heartbeat.ProbeFailureThreshold)
	}

	// Past the hold-off the next failing result transitions it down, once.
	harness.clock.Advance(heartbeat.StartupHoldOff)
	harness.makeDue(testInstance, probe.ID)
	harness.scan(testInstance)
	harness.prober.Wait()
	down := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateDown, heartbeat.ProbeFailureThreshold+1)
	if down.DownReason == nil || *down.DownReason != heartbeat.ReasonHTTPStatus {
		testInstance.Fatalf("down reason = %v, want %q", down.DownReason, heartbeat.ReasonHTTPStatus)
	}
	harness.tick(testInstance)
	harness.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, harness.receiver.received(), 0, "heartbeat.down", heartbeat.ReasonHTTPStatus)
	harness.requireQuiet(testInstance, 2)
}

func TestProberRecoversADownCheckWithOneRecoveredAlert(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()
	harness.failPasses(testInstance, heartbeat.ProbeFailureThreshold)
	harness.tick(testInstance)
	harness.receiver.waitForRequests(testInstance, 2)

	harness.succeedPass(testInstance)

	recovered := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateUp, 0)
	if recovered.Probe.DownDetail != nil {
		testInstance.Fatalf("down detail after recovery = %q, want none", *recovered.Probe.DownDetail)
	}
	harness.tick(testInstance)
	harness.receiver.waitForRequests(testInstance, 4)
	requirePathsAndType(testInstance, harness.receiver.received(), 2, "heartbeat.recovered", "")
	bodies := displayBodies(testInstance, harness.receiver.received(), 2)
	if len(bodies) != 2 || !strings.HasPrefix(bodies[0], "Probe succeeded at ") || !strings.HasPrefix(bodies[1], "Probe succeeded at ") {
		testInstance.Fatalf("recovery bodies = %q, want Probe succeeded at ...", bodies)
	}
	harness.requireQuiet(testInstance, 4)
}

func TestProberFailureOnAMissedCheckOverwritesItsReasonWithoutAnotherAlert(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()

	// Past period + derived grace with no Scan: the checker takes it down as missed.
	harness.clock.Advance(heartbeat.MinProbeIntervalSeconds * (heartbeat.ProbeFailureThreshold + 2) * time.Second)
	harness.tick(testInstance)
	harness.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, harness.receiver.received(), 0, "heartbeat.down", heartbeat.ReasonMissed)
	baseline := len(harness.receiver.received())
	missed := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateDown, 0)
	if missed.DownReason == nil || *missed.DownReason != heartbeat.ReasonMissed {
		testInstance.Fatalf("down reason = %v, want %q", missed.DownReason, heartbeat.ReasonMissed)
	}

	harness.target.status.Store(http.StatusServiceUnavailable)
	harness.scan(testInstance)
	harness.prober.Wait()

	after := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateDown, 1)
	if after.DownReason == nil || *after.DownReason != heartbeat.ReasonHTTPStatus ||
		after.Probe.DownDetail == nil || *after.Probe.DownDetail != "HTTP 503" {
		testInstance.Fatalf("after the probe result: reason %v detail %v, want %q and %q",
			after.DownReason, after.Probe.DownDetail, heartbeat.ReasonHTTPStatus, "HTTP 503")
	}
	if after.PendingAlert != nil {
		testInstance.Fatalf("pending alert = %q, want none", *after.PendingAlert)
	}
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, baseline)
}

func TestProberSkipsAPausedCheck(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()
	if _, pauseErr := harness.service.Pause(harness.ctx, probe.ID); pauseErr != nil {
		testInstance.Fatalf("Pause: %v", pauseErr)
	}

	harness.pass(testInstance)
	harness.pass(testInstance)

	if got := harness.target.requests.Load(); got != 0 {
		testInstance.Fatalf("target saw %d requests for a paused check, want 0", got)
	}

	// Control: the same check is probed once resumed.
	if _, resumeErr := harness.service.Resume(harness.ctx, probe.ID); resumeErr != nil {
		testInstance.Fatalf("Resume: %v", resumeErr)
	}
	harness.pass(testInstance)
	if got := harness.target.requests.Load(); got != 1 {
		testInstance.Fatalf("target saw %d requests after resume, want 1", got)
	}
}

// staleInFlight claims a probe, holds its request open, applies change to the check, then releases
// the target with a failing status and asserts the late result was discarded.
func staleInFlight(testInstance *testing.T, change func(*proberHarness, heartbeat.StoredCheck)) (*proberHarness, heartbeat.StoredCheck) {
	testInstance.Helper()
	harness := newProberHarness(testInstance)
	probe := harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()
	harness.succeedPass(testInstance)

	harness.target.block()
	harness.target.status.Store(http.StatusInternalServerError)
	harness.clock.Advance(proberPassStep)
	harness.scan(testInstance)
	harness.target.waitArrival(testInstance)

	change(harness, probe)
	harness.target.release()
	harness.prober.Wait()
	// The success pass and the held probe: proves the late request really was dispatched.
	if got := harness.target.requests.Load(); got != 2 {
		testInstance.Fatalf("target saw %d requests, want 2 (the success pass and the held probe)", got)
	}
	return harness, probe
}

// requireResultDiscarded asserts a check still reads as it did before the held probe answered.
func requireResultDiscarded(testInstance *testing.T, harness *proberHarness, probeID int64, before heartbeat.StoredCheck) {
	testInstance.Helper()
	after := harness.get(testInstance, probeID)
	if after.State != before.State || after.Probe.ConsecutiveFailures != before.Probe.ConsecutiveFailures ||
		!reflect.DeepEqual(after.Probe.LastResult, before.Probe.LastResult) || after.PendingAlert != nil {
		testInstance.Fatalf("the stale result was applied: before state %q failures %d last %+v; after state %q failures %d last %+v pending %v",
			before.State, before.Probe.ConsecutiveFailures, before.Probe.LastResult,
			after.State, after.Probe.ConsecutiveFailures, after.Probe.LastResult, after.PendingAlert)
	}
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, 0)
}

func TestProberDiscardsAResultForACheckPausedWhileInFlight(testInstance *testing.T) {
	var before heartbeat.StoredCheck
	harness, probe := staleInFlight(testInstance, func(harness *proberHarness, probe heartbeat.StoredCheck) {
		paused, pauseErr := harness.service.Pause(harness.ctx, probe.ID)
		if pauseErr != nil {
			testInstance.Fatalf("Pause: %v", pauseErr)
		}
		before = paused
	})
	requireResultDiscarded(testInstance, harness, probe.ID, before)
}

func TestProberDiscardsAResultForACheckDeletedWhileInFlight(testInstance *testing.T) {
	harness, probe := staleInFlight(testInstance, func(harness *proberHarness, probe heartbeat.StoredCheck) {
		if deleted, deleteErr := harness.service.Delete(harness.ctx, probe.ID); deleteErr != nil || !deleted {
			testInstance.Fatalf("Delete: deleted %v err %v", deleted, deleteErr)
		}
	})
	if _, getErr := harness.store.Get(harness.ctx, probe.ID); !errors.Is(getErr, heartbeat.ErrCheckNotFound) {
		testInstance.Fatalf("Get after delete = %v, want ErrCheckNotFound", getErr)
	}
	harness.tick(testInstance)
	harness.requireQuiet(testInstance, 0)
}

func TestProberDiscardsAResultForAnEditedConfigVersion(testInstance *testing.T) {
	var before heartbeat.StoredCheck
	harness, probe := staleInFlight(testInstance, func(harness *proberHarness, probe heartbeat.StoredCheck) {
		// Simulates an edit: Patch re-validates the merged config and rejects the stored IP-literal URL.
		if _, execErr := harness.pool.Exec(harness.ctx,
			`UPDATE heartbeat_check_probes SET config_version = config_version + 1 WHERE check_id = $1`, probe.ID); execErr != nil {
			testInstance.Fatalf("bump config_version: %v", execErr)
		}
		before = harness.get(testInstance, probe.ID)
		if before.Probe.ConfigVersion == probe.Probe.ConfigVersion {
			testInstance.Fatalf("config_version did not change")
		}
	})
	requireResultDiscarded(testInstance, harness, probe.ID, before)
}

func TestProberDoesNotStartACheckWhoseProbeIsStillInFlight(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	harness.create(testInstance, e2eCheckName, nil)
	harness.pastHoldOff()

	harness.target.block()
	harness.clock.Advance(proberPassStep)
	harness.scan(testInstance)
	harness.target.waitArrival(testInstance)

	// The claim moved next_probe_at one interval ahead, so the clock is left where it is.
	harness.scan(testInstance)
	if got := harness.target.requests.Load(); got != 1 {
		testInstance.Fatalf("target saw %d requests while the first probe is in flight, want 1", got)
	}
	harness.target.release()
	harness.prober.Wait()
	// Wait has drained every started probe, so a second start would show here.
	if got := harness.target.requests.Load(); got != 1 {
		testInstance.Fatalf("target saw %d requests after the release, want 1", got)
	}
}

func TestProberRecordsAConfigFailureForAnUnopenableTokenWithoutRequesting(testInstance *testing.T) {
	harness := newProberHarness(testInstance)
	first := harness.create(testInstance, "alpha", &heartbeat.AccessToken{ClientID: "id-alpha", ClientSecret: "secret-alpha"})
	second := harness.create(testInstance, "beta", &heartbeat.AccessToken{ClientID: "id-beta", ClientSecret: "secret-beta"})

	// Swap the sealed headers so each is sealed under the other's check id: a wrong-AAD row, not a
	// garbage blob that could fail vacuously.
	sealed := map[int64][]byte{}
	for _, probeID := range []int64{first.ID, second.ID} {
		var blob []byte
		if scanErr := harness.pool.QueryRow(harness.ctx,
			`SELECT sealed_access_headers FROM heartbeat_check_probes WHERE check_id = $1`, probeID).Scan(&blob); scanErr != nil {
			testInstance.Fatalf("read sealed headers %d: %v", probeID, scanErr)
		}
		sealed[probeID] = blob
	}
	for target, source := range map[int64]int64{first.ID: second.ID, second.ID: first.ID} {
		if _, execErr := harness.pool.Exec(harness.ctx,
			`UPDATE heartbeat_check_probes SET sealed_access_headers = $1 WHERE check_id = $2`, sealed[source], target); execErr != nil {
			testInstance.Fatalf("swap sealed headers into %d: %v", target, execErr)
		}
	}
	harness.pastHoldOff()

	for range heartbeat.ProbeFailureThreshold {
		harness.pass(testInstance)
	}

	if got := harness.target.requests.Load(); got != 0 {
		testInstance.Fatalf("target saw %d requests, want 0 (the credential never opened)", got)
	}
	for _, probe := range []heartbeat.StoredCheck{first, second} {
		down := harness.requireProbeState(testInstance, probe.ID, heartbeat.StateDown, heartbeat.ProbeFailureThreshold)
		requireLastResultReason(testInstance, down, heartbeat.ReasonConfig)
		if down.DownReason == nil || *down.DownReason != heartbeat.ReasonConfig {
			testInstance.Fatalf("%s down reason = %v, want %q", probe.Name, down.DownReason, heartbeat.ReasonConfig)
		}
	}
	harness.tick(testInstance)
	harness.receiver.waitForRequests(testInstance, 4)
	counts := webhookCounts(testInstance, harness.receiver.received())
	want := map[string]int{"heartbeat.down|alpha": 2, "heartbeat.down|beta": 2}
	if !reflect.DeepEqual(counts, want) {
		testInstance.Fatalf("alerts = %v, want one down alert per check and destination %v", counts, want)
	}
	harness.requireQuiet(testInstance, 4)
}
