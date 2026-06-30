package services

import (
	"os"
	"strings"
	"testing"
)

// The embedded artifacts are recreated by the agent, so they MUST stay byte-for-byte
// in sync with what elchi-install.sh writes. This test fails if the installer is
// edited without updating the constants here (or vice versa).
func TestLogrotateContentMatchesInstaller(t *testing.T) {
	data, err := os.ReadFile("../../elchi-install.sh")
	if err != nil {
		t.Skipf("installer not readable from test cwd (%v); skipping drift check", err)
	}
	installer := string(data)

	for _, a := range logrotateArtifacts() {
		if !strings.Contains(installer, a.content) {
			t.Errorf("embedded content for %s not found verbatim in elchi-install.sh — they have drifted apart:\n---embedded---\n%s", a.path, a.content)
		}
	}
}

func TestLogrotateArtifactsWellFormed(t *testing.T) {
	for _, a := range logrotateArtifacts() {
		if a.path == "" || a.content == "" || a.mode == "" {
			t.Errorf("malformed artifact: %+v", a)
		}
		if !strings.HasPrefix(a.path, "/") {
			t.Errorf("artifact path must be absolute: %q", a.path)
		}
	}
}
