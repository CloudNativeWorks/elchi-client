package systemd

import "testing"

// After a successful start/restart/reload, only definitively-bad states should
// be reported as failures; transient states must pass to avoid false failures.
func TestIsFailedActiveState(t *testing.T) {
	failed := []string{"failed", "inactive"}
	for _, s := range failed {
		if !isFailedActiveState(s) {
			t.Errorf("state %q should be treated as failed", s)
		}
	}

	ok := []string{"active", "activating", "reloading", "deactivating", "", "unknown"}
	for _, s := range ok {
		if isFailedActiveState(s) {
			t.Errorf("state %q must NOT be treated as a failure", s)
		}
	}
}
