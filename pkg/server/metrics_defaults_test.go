package server

import "testing"

func TestDefaultConfigDisablesMetrics(t *testing.T) {
	t.Parallel()

	if got := DefaultConfig().MetricsAddr; got != "" {
		t.Fatalf("DefaultConfig().MetricsAddr = %q, want disabled", got)
	}
}
