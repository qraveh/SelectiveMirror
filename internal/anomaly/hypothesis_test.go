package anomaly

import "testing"

func TestHypothesisFor_AllKinds(t *testing.T) {
	kinds := []Kind{
		KindPanic, KindCircuitBreaker, KindWatcherError, KindQueueDepthWarning,
		KindGhostLeak, KindGhostOrphan, KindGhostStale, KindReconcileStale,
		KindPathGone, KindSyncTimeout, KindSyncFailure,
	}
	for _, k := range kinds {
		h := HypothesisFor(k)
		if h == "" {
			t.Errorf("HypothesisFor(%q) returned empty string", k)
		}
	}
}

func TestHypothesisFor_UnknownKind(t *testing.T) {
	h := HypothesisFor(Kind("totally-unknown"))
	if h != "" {
		t.Errorf("expected empty for unknown kind, got %q", h)
	}
}
