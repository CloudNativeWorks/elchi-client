package network

import "testing"

// The safety guard that prevents flushTableRoutesAndRules from ever touching
// the kernel's system tables (main=254, local=255, default=253).
func TestIsElchiManagedTableID(t *testing.T) {
	managed := []int{MinTableID, MaxTableID, 100, 500, 999}
	for _, id := range managed {
		if !isElchiManagedTableID(id) {
			t.Errorf("table %d should be Elchi-managed", id)
		}
	}

	system := []int{0, 99, 1000, 253, 254, 255}
	for _, id := range system {
		if isElchiManagedTableID(id) {
			t.Errorf("table %d must NOT be considered Elchi-managed (system/out-of-range)", id)
		}
	}
}
