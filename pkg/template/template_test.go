package template

import (
	"fmt"
	"strings"
	"testing"
)

// SystemdTemplate is rendered with fmt.Sprintf at several call sites with a fixed
// arg list (7 strings + 1 int). If the verb count/types ever drift from the call
// sites, Go emits "%!" markers and the unit file is silently corrupted. This pins
// the contract: rendering with the documented arg shape must be clean.
func TestSystemdTemplateRendersCleanly(t *testing.T) {
	out := fmt.Sprintf(SystemdTemplate,
		"web",    // Description
		"1.34.0", // ExecStartPre envoy path
		"web-80", // ExecStartPre bootstrap
		"1.34.0", // ExecStart envoy path
		"web-80", // ExecStart bootstrap
		80,       // base-id
		"web-80", // log path
		"web-80", // SyslogIdentifier
	)
	if strings.Contains(out, "%!") {
		t.Fatalf("SystemdTemplate has a verb/arg mismatch:\n%s", out)
	}
	for _, want := range []string{"web", "1.34.0", "80"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered unit missing %q", want)
		}
	}
}

func TestDummyNetPlanRendersCleanly(t *testing.T) {
	out := fmt.Sprintf(DummyNetPlan, "elchi-if-80", "10.0.0.1/32")
	if strings.Contains(out, "%!") {
		t.Fatalf("DummyNetPlan has a verb/arg mismatch:\n%s", out)
	}
	for _, want := range []string{"elchi-if-80", "10.0.0.1/32"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered netplan missing %q", want)
		}
	}
}

// PythonWrapper is written verbatim (NOT through Sprintf); its %H/%M/%S are Python
// strftime specifiers. Guard that it stays a real python script and is never
// accidentally turned into a format string with leftover Go verbs.
func TestPythonWrapperIsRawScript(t *testing.T) {
	if !strings.HasPrefix(PythonWrapper, "#!/usr/bin/env python3") {
		t.Errorf("PythonWrapper should start with a python3 shebang")
	}
	if strings.Contains(PythonWrapper, "%!") {
		t.Errorf("PythonWrapper contains a malformed-verb marker")
	}
}
