package tools

import "testing"

// GetIPv4CIDR turns a bare IPv4 into a /32 and is used on the deploy path to derive
// the dummy-interface address. A wrong accept/reject here means a deploy either uses
// a malformed address or rejects a valid one.
func TestGetIPv4CIDR(t *testing.T) {
	t.Run("valid IPv4 becomes /32", func(t *testing.T) {
		got, err := GetIPv4CIDR("10.0.0.1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "10.0.0.1/32" {
			t.Errorf("got %q, want 10.0.0.1/32", got)
		}
	})

	rejected := map[string]string{
		"CIDR notation": "10.0.0.1/24",
		"empty":         "",
		"garbage":       "not-an-ip",
		"ipv6":          "2001:db8::1",
		"trailing junk": "10.0.0.1x",
		"out of range":  "999.0.0.1",
	}
	for name, in := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := GetIPv4CIDR(in); err == nil {
				t.Errorf("GetIPv4CIDR(%q) should have errored", in)
			}
		})
	}
}
