package anomaly

// HypothesisFor returns a causal hypothesis template for the given anomaly kind.
// These are human-readable investigation starting points, not diagnoses.
func HypothesisFor(k Kind) string {
	switch k {
	case KindPanic:
		return "Code bug: check stack trace for nil pointer, index out of range, or type assertion failure. Is this reproducible with the same file?"
	case KindCircuitBreaker:
		return "Mirror unreachable: network down? Auth token expired? Remote path deleted? Backend quota exceeded? Check rclone logs."
	case KindWatcherError:
		return "Filesystem watcher error: too many open handles? OS watch limit reached? Filesystem unmounted or disconnected?"
	case KindQueueDepthWarning:
		return "Queue overloaded: burst of file changes? Workers blocked by slow backend? Circuit breaker pausing a mirror?"
	case KindGhostLeak:
		return "Filter leak: file was synced before exclusion rule was added. Was .syncignore recently updated? Is auto-cleanup (SM-069) working?"
	case KindGhostOrphan:
		return "Unexpected remote file: batch reconciliation artifact? Manual upload? Was this file synced then its state DB entry lost?"
	case KindGhostStale:
		return "Stale remote file: rename/move residue? Was the file moved to a new path? Check state DB for the old path."
	case KindReconcileStale:
		return "Reconciliation not completing: worker starvation? Deadlock? rclone hanging? Check queue depth and circuit breaker state."
	case KindPathGone:
		return "Mirror path disappeared: drive disconnected? Directory deleted? Symlink broken?"
	case KindSyncTimeout:
		return "Sync timed out (5 min): very large file? Network stall? Backend throttling? Check bandwidth_limit setting."
	case KindSyncFailure:
		return "Sync failed: check rclone exit code. Exit 1=general, 3=dir not found, 5=transient, 7=fatal. Is the file accessible locally?"
	default:
		return ""
	}
}
