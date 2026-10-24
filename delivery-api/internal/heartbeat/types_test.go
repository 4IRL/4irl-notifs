package heartbeat

import (
	"reflect"
	"slices"
	"testing"
)

func TestReasonSets(t *testing.T) {
	probeReasons := []string{
		ReasonHTTPStatus, ReasonTimeout, ReasonBodyMismatch, ReasonBlocked,
		ReasonTLS, ReasonScheme, ReasonNetwork, ReasonConfig,
	}
	cases := []struct {
		reason      string
		probeResult bool
		known       bool
	}{
		{ReasonHTTPStatus, true, true},
		{ReasonTimeout, true, true},
		{ReasonBodyMismatch, true, true},
		{ReasonBlocked, true, true},
		{ReasonTLS, true, true},
		{ReasonScheme, true, true},
		{ReasonNetwork, true, true},
		{ReasonConfig, true, true},
		{ReasonMissed, false, true},
		{ReasonFail, false, true},
		{"bogus", false, false},
		{"", false, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.reason, func(t *testing.T) {
			if got := IsProbeResultReason(testCase.reason); got != testCase.probeResult {
				t.Fatalf("IsProbeResultReason(%q) = %v, want %v", testCase.reason, got, testCase.probeResult)
			}
			if got := IsKnownDownReason(testCase.reason); got != testCase.known {
				t.Fatalf("IsKnownDownReason(%q) = %v, want %v", testCase.reason, got, testCase.known)
			}
		})
	}

	known := KnownDownReasons()
	if len(known) != 10 {
		t.Fatalf("KnownDownReasons has %d entries, want 10: %v", len(known), known)
	}
	want := append([]string{ReasonMissed, ReasonFail}, probeReasons...)
	slices.Sort(want)
	got := slices.Clone(known)
	slices.Sort(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KnownDownReasons = %v, want %v", got, want)
	}
	known[0] = "mutated"
	if KnownDownReasons()[0] == "mutated" {
		t.Fatal("KnownDownReasons must return a fresh slice")
	}
}

func TestStoredCheckKind(t *testing.T) {
	if got := (StoredCheck{}).Kind(); got != KindPush {
		t.Fatalf("Kind() = %q, want %q", got, KindPush)
	}
	if got := (StoredCheck{Probe: &ProbeState{}}).Kind(); got != KindProbe {
		t.Fatalf("Kind() = %q, want %q", got, KindProbe)
	}
}

func TestCheckPatchIsEmptyWithProbeFields(t *testing.T) {
	cases := []struct {
		name  string
		patch CheckPatch
		empty bool
	}{
		{"zero", CheckPatch{}, true},
		{"empty non-nil destinations", CheckPatch{DestinationNames: []string{}}, false},
		{"probe only", CheckPatch{Probe: &ProbePatch{}}, false},
		{"token replace only", CheckPatch{AccessToken: AccessTokenPatch{Action: AccessTokenReplace}}, false},
		{"token clear only", CheckPatch{AccessToken: AccessTokenPatch{Action: AccessTokenClear}}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.patch.IsEmpty(); got != testCase.empty {
				t.Fatalf("IsEmpty() = %v, want %v", got, testCase.empty)
			}
		})
	}
}

func probeCheckForTest() StoredCheck {
	body := "ok"
	return StoredCheck{
		PeriodSeconds: 300,
		GraceSeconds:  1200,
		Probe: &ProbeState{Config: ProbeConfig{
			URL:              "https://example.com/health",
			ExpectedStatuses: []int{204, 200},
			BodyMatch:        &body,
			TimeoutSeconds:   5,
			IntervalSeconds:  300,
		}},
	}
}

func TestApplyPatchMergesProbeFieldByField(t *testing.T) {
	current := probeCheckForTest()
	newURL := "https://other.example.com/"
	newInterval := 600
	update := ApplyPatch(current, CheckPatch{Probe: &ProbePatch{
		URL:              &newURL,
		ExpectedStatuses: []int{503, 200},
		IntervalSeconds:  &newInterval,
	}})
	if update.Probe == nil {
		t.Fatal("expected merged probe config")
	}
	merged := *update.Probe
	if merged.URL != newURL || merged.IntervalSeconds != 600 || merged.TimeoutSeconds != 5 {
		t.Fatalf("merged = %+v", merged)
	}
	if !reflect.DeepEqual(merged.ExpectedStatuses, []int{200, 503}) {
		t.Fatalf("statuses = %v, want ascending [200 503]", merged.ExpectedStatuses)
	}
	if merged.BodyMatch == nil || *merged.BodyMatch != "ok" {
		t.Fatalf("body match should be kept, got %v", merged.BodyMatch)
	}
	if update.PeriodSeconds != 600 || update.GraceSeconds != 2400 {
		t.Fatalf("derived period/grace = %d/%d, want 600/2400", update.PeriodSeconds, update.GraceSeconds)
	}
	if !reflect.DeepEqual(current.Probe.Config.ExpectedStatuses, []int{204, 200}) {
		t.Fatalf("current statuses were mutated: %v", current.Probe.Config.ExpectedStatuses)
	}
}

func TestApplyPatchClearBodyMatchWinsAndKeepsRest(t *testing.T) {
	current := probeCheckForTest()
	replacement := "new"
	update := ApplyPatch(current, CheckPatch{
		Probe:       &ProbePatch{BodyMatch: &replacement, ClearBodyMatch: true},
		AccessToken: AccessTokenPatch{Action: AccessTokenClear},
	})
	if update.Probe.BodyMatch != nil {
		t.Fatalf("ClearBodyMatch must win, got %q", *update.Probe.BodyMatch)
	}
	if update.AccessToken.Action != AccessTokenClear {
		t.Fatalf("token patch not copied: %+v", update.AccessToken)
	}
	if !reflect.DeepEqual(update.Probe.ExpectedStatuses, []int{200, 204}) {
		t.Fatalf("statuses = %v", update.Probe.ExpectedStatuses)
	}
}

func TestApplyPatchOnPushCheckLeavesProbeNil(t *testing.T) {
	current := StoredCheck{PeriodSeconds: 60, GraceSeconds: 60}
	update := ApplyPatch(current, CheckPatch{Probe: &ProbePatch{}})
	if update.Probe != nil {
		t.Fatalf("push check must keep Probe nil, got %+v", update.Probe)
	}
	if update.PeriodSeconds != 60 || update.GraceSeconds != 60 {
		t.Fatalf("period/grace changed: %+v", update)
	}
}

func TestProbeConfigChanged(t *testing.T) {
	base := probeCheckForTest().Probe.Config
	otherBody := "other"
	sameBody := "ok"
	clone := func() ProbeConfig {
		config := base
		config.ExpectedStatuses = slices.Clone(base.ExpectedStatuses)
		return config
	}
	cases := []struct {
		name    string
		mutate  func(*ProbeConfig)
		token   AccessTokenPatch
		changed bool
	}{
		{"identical", func(*ProbeConfig) {}, AccessTokenPatch{}, false},
		{"reordered statuses", func(c *ProbeConfig) { c.ExpectedStatuses = []int{200, 204} }, AccessTokenPatch{}, false},
		{"equal body by value", func(c *ProbeConfig) { c.BodyMatch = &sameBody }, AccessTokenPatch{}, false},
		{"url", func(c *ProbeConfig) { c.URL = "https://x.example.com" }, AccessTokenPatch{}, true},
		{"statuses", func(c *ProbeConfig) { c.ExpectedStatuses = []int{200} }, AccessTokenPatch{}, true},
		{"body differs", func(c *ProbeConfig) { c.BodyMatch = &otherBody }, AccessTokenPatch{}, true},
		{"body cleared", func(c *ProbeConfig) { c.BodyMatch = nil }, AccessTokenPatch{}, true},
		{"timeout", func(c *ProbeConfig) { c.TimeoutSeconds = 6 }, AccessTokenPatch{}, true},
		{"interval", func(c *ProbeConfig) { c.IntervalSeconds = 360 }, AccessTokenPatch{}, true},
		{"token replace", func(*ProbeConfig) {}, AccessTokenPatch{Action: AccessTokenReplace}, true},
		{"token clear", func(*ProbeConfig) {}, AccessTokenPatch{Action: AccessTokenClear}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			merged := clone()
			testCase.mutate(&merged)
			if got := ProbeConfigChanged(base, merged, testCase.token); got != testCase.changed {
				t.Fatalf("ProbeConfigChanged = %v, want %v", got, testCase.changed)
			}
		})
	}
}
