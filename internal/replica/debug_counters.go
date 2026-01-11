package replica

import "sync/atomic"

// Global Debug Counters (for investigating data loss)
var (
	DebugTotalParsedRDB     atomic.Int64
	DebugTotalParsedJournal atomic.Int64
	DebugTotalEnqueued      atomic.Int64
	DebugTotalFlushed       atomic.Int64
)
