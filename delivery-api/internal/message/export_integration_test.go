//go:build integration

package message

// The store queries whose plans the external integration tests EXPLAIN, exposed
// so they check exactly what the store runs.
const (
	ScheduledCountSQL = scheduledCountSQL
	ReleaseProbeSQL   = releaseProbeSQL
	ScheduledListSQL  = scheduledListSQL
	ScheduledTotalSQL = scheduledTotalSQL
	DeleteExpiredSQL  = deleteExpiredSQL
)
