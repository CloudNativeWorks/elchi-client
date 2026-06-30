package network

import (
	"strings"
	"testing"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
)

// The persistWarnings accumulator is how a kernel-applied-but-not-persisted route /
// policy is surfaced to the control plane instead of being a silent success. Pin
// that it starts empty and records each warning verbatim.
func TestRoutePersistWarnings(t *testing.T) {
	rm := newTestRouteManager(t)
	if len(rm.PersistWarnings()) != 0 {
		t.Fatal("a fresh manager must have no warnings")
	}

	rm.addPersistWarning("route to %s not persisted: %v", "10.0.0.0/24", "disk full")
	rm.addPersistWarning("route to %s not persisted: %v", "10.0.1.0/24", "disk full")

	w := rm.PersistWarnings()
	if len(w) != 2 {
		t.Fatalf("expected 2 warnings, got %d", len(w))
	}
	if !strings.Contains(w[0], "10.0.0.0/24") || !strings.Contains(w[1], "10.0.1.0/24") {
		t.Errorf("warnings not recorded verbatim: %v", w)
	}
}

func TestPolicyPersistWarnings(t *testing.T) {
	if err := logger.Init(logger.Config{Level: "error", Format: "text", Module: "test"}); err != nil {
		t.Fatalf("logger init: %v", err)
	}
	pm := NewPolicyManager(logger.NewLogger("policy-test"))
	if len(pm.PersistWarnings()) != 0 {
		t.Fatal("a fresh policy manager must have no warnings")
	}

	pm.addPersistWarning("policy from=%s not persisted", "10.0.0.0/8")
	if w := pm.PersistWarnings(); len(w) != 1 || !strings.Contains(w[0], "10.0.0.0/8") {
		t.Errorf("policy warning not recorded: %v", w)
	}
}
