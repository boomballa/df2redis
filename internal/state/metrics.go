package state

const (
	MetricSourceKeysEstimated   = "source.keys.estimated"
	MetricTargetKeysInitial     = "target.keys.initial"
	MetricTargetKeysCurrent     = "target.keys.current"
	MetricSyncedKeys            = "sync.keys.applied"
	MetricFlowImportedFormat    = "flow.%d.imported_keys"
	MetricCheckpointSavedAtUnix = "checkpoint.last_saved_unix"

	// RDB phase metrics (snapshot import)
	MetricRdbOpsTotal          = "sync.rdb.ops.total"
	MetricRdbOpsSuccess        = "sync.rdb.ops.success"
	MetricRdbInlineJournalOps  = "sync.rdb.inline_journal.ops" // Inline journal entries applied during RDB

	// Incremental phase metrics (journal streaming)
	MetricIncrementalLSNCurrent = "sync.incremental.lsn.current"
	MetricIncrementalLSNApplied = "sync.incremental.lsn.applied"
	MetricIncrementalLagMs      = "sync.incremental.lag.ms"
	MetricIncrementalOpsTotal   = "sync.incremental.ops.total"
	MetricIncrementalOpsSuccess = "sync.incremental.ops.success"
	MetricIncrementalOpsSkipped = "sync.incremental.ops.skipped"
	MetricIncrementalOpsFailed  = "sync.incremental.ops.failed"
)
