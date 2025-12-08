package state

const (
	MetricSourceKeysEstimated   = "source.keys.estimated"
	MetricTargetKeysInitial     = "target.keys.initial"
	MetricTargetKeysCurrent     = "target.keys.current"
	MetricSyncedKeys            = "sync.keys.applied"
	MetricFlowImportedFormat    = "flow.%d.imported_keys"
	MetricCheckpointSavedAtUnix = "checkpoint.last_saved_unix"
	MetricIncrementalLSNCurrent = "sync.incremental.lsn.current"
	MetricIncrementalLSNApplied = "sync.incremental.lsn.applied"
	MetricIncrementalLagMs      = "sync.incremental.lag.ms"
	MetricIncrementalOpsTotal   = "sync.incremental.ops.total"
	MetricIncrementalOpsSuccess = "sync.incremental.ops.success"
	MetricIncrementalOpsSkipped = "sync.incremental.ops.skipped"
	MetricIncrementalOpsFailed  = "sync.incremental.ops.failed"
)
