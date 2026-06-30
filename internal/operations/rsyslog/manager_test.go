package rsyslog

import (
	"strings"
	"testing"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func rsyslogReq(target, proto string, port int32) *client.RequestRsyslog {
	return &client.RequestRsyslog{
		RsyslogConfig: &client.RsyslogConfig{
			RsyslogOutput: &client.RsyslogOutput{Target: target, Port: port, Protocol: proto},
		},
	}
}

// RenderConfig is the single source of truth for 50-elchi.conf, used both to write
// the file AND by the reconcile loop to detect drift. It must be byte-stable for the
// same input (else reconcile would flap), and must reject invalid input rather than
// emit a broken config.
func TestRsyslogRenderConfigDeterministicAndValid(t *testing.T) {
	req := rsyslogReq("10.0.0.1", "tcp", 514)
	first, err := RenderConfig(req)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for i := 0; i < 10; i++ {
		again, err := RenderConfig(req)
		if err != nil || again != first {
			t.Fatalf("RenderConfig not deterministic (i=%d, err=%v)", i, err)
		}
	}
	for _, want := range []string{`target="10.0.0.1"`, `port="514"`, `protocol="tcp"`, "imfile"} {
		if !strings.Contains(first, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
}

func TestRsyslogRenderConfigRejectsInvalid(t *testing.T) {
	cases := map[string]*client.RequestRsyslog{
		"empty target":  rsyslogReq("", "tcp", 514),
		"bad protocol":  rsyslogReq("10.0.0.1", "sctp", 514),
		"port too high": rsyslogReq("10.0.0.1", "tcp", 70000),
		"port zero":     rsyslogReq("10.0.0.1", "tcp", 0),
		"nil rsyslog":   {},
	}
	for name, req := range cases {
		if _, err := RenderConfig(req); err == nil {
			t.Errorf("%s: expected an error, got nil", name)
		}
	}
}

func TestExtractQuotedValue(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		key     string
		wantVal string
		wantOK  bool
	}{
		{"normal", `  target="1.2.3.4"`, "target=", "1.2.3.4", true},
		{"port numeric", `  port="5044"`, "port=", "5044", true},
		{"protocol", `  protocol="tcp"`, "protocol=", "tcp", true},
		{"key absent", `  something="x"`, "target=", "", false},
		// The historical panic: an unquoted, hand-edited value.
		{"unquoted value", `target=1.2.3.4`, "target=", "", false},
		{"missing closing quote", `target="1.2.3.4`, "target=", "", false},
		{"empty line", ``, "target=", "", false},
		{"empty quoted value", `target=""`, "target=", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, ok := extractQuotedValue(tc.line, tc.key)
			if val != tc.wantVal || ok != tc.wantOK {
				t.Fatalf("extractQuotedValue(%q,%q) = (%q,%v), want (%q,%v)",
					tc.line, tc.key, val, ok, tc.wantVal, tc.wantOK)
			}
		})
	}
}

func TestParseRsyslogConfig_Valid(t *testing.T) {
	data := `module(load="imfile")
action(
  type="omfwd"
  target="10.0.0.5"
  port="5044"
  protocol="tcp"
)
`
	got := parseRsyslogConfig(data)
	out := got.GetRsyslogConfig().GetRsyslogOutput()
	if out.GetTarget() != "10.0.0.5" {
		t.Errorf("target = %q, want 10.0.0.5", out.GetTarget())
	}
	if out.GetPort() != 5044 {
		t.Errorf("port = %d, want 5044", out.GetPort())
	}
	if out.GetProtocol() != "tcp" {
		t.Errorf("protocol = %q, want tcp", out.GetProtocol())
	}
}

// The key regression guard: a hand-edited / malformed file must never panic.
func TestParseRsyslogConfig_MalformedDoesNotPanic(t *testing.T) {
	inputs := []string{
		"target=1.2.3.4\nport=5044\nprotocol=tcp", // unquoted (used to panic)
		`target="1.2.3.4`,                         // unterminated quote
		"port=\nprotocol=",                        // empty values, no quotes
		"#target=\"x\"\n\n   \n",                  // comments + blanks only
		"\x00\x01garbage target= port= protocol=", // binary garbage
		"", // empty file
	}
	for i, in := range inputs {
		// A panic here fails the test rather than crashing the suite.
		got := parseRsyslogConfig(in)
		if got == nil || got.GetRsyslogConfig() == nil || got.GetRsyslogConfig().GetRsyslogOutput() == nil {
			t.Fatalf("input %d: parse returned an incomplete proto", i)
		}
	}
}
