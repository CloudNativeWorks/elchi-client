package services

import (
	"strings"
	"testing"
)

// envoyBinaryPath / missingBinaryError are the basis of the actionable
// "binary missing" signal that replaced the opaque "failed to restart service".
// The error must name the version + the SET_VERSION recovery step so the control
// plane / operator knows the fix is "push the binary", not "retry the deploy".
func TestEnvoyBinaryPath(t *testing.T) {
	got := envoyBinaryPath("1.34.0")
	if !strings.HasPrefix(got, "/var/lib/elchi/envoys/1.34.0/") || !strings.HasSuffix(got, "/envoy") {
		t.Errorf("unexpected binary path: %q", got)
	}
}

func TestMissingBinaryErrorIsActionable(t *testing.T) {
	err := missingBinaryError("1.34.0")
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, want := range []string{"1.34.0", "SET_VERSION", envoyBinaryPath("1.34.0")} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message should contain %q, got: %s", want, msg)
		}
	}
}
