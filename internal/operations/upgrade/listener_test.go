package upgrade

import (
	"strings"
	"testing"
)

func TestEnvoyBinaryPath(t *testing.T) {
	if got := envoyBinaryPath("1.34.1"); got != "/var/lib/elchi/envoys/1.34.1/envoy" {
		t.Fatalf("envoyBinaryPath = %q", got)
	}
}

func TestReplaceEnvoyVersionPath(t *testing.T) {
	content := `ExecStart=/var/lib/elchi/envoys/1.34.0/envoy -c /x.yaml
ExecStartPre=/var/lib/elchi/envoys/1.34.0/envoy --version`
	got := replaceEnvoyVersionPath(content, "1.34.0", "1.34.1")

	if strings.Contains(got, "envoys/1.34.0/envoy") {
		t.Fatalf("old version path still present:\n%s", got)
	}
	if strings.Count(got, "envoys/1.34.1/envoy") != 2 {
		t.Fatalf("expected both occurrences replaced:\n%s", got)
	}
}

func TestReplaceEnvoyVersionPath_NoMatchIsNoop(t *testing.T) {
	content := "ExecStart=/var/lib/elchi/envoys/9.9.9/envoy -c /x.yaml"
	if got := replaceEnvoyVersionPath(content, "1.0.0", "2.0.0"); got != content {
		t.Fatalf("content changed unexpectedly:\n%s", got)
	}
}
