package shield

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestParsePromGauge(t *testing.T) {
	body := `# HELP elchi_shield_config_reload_failures_consecutive consecutive failures
# TYPE elchi_shield_config_reload_failures_consecutive gauge
elchi_shield_config_reload_failures_consecutive{instance="h-shield"} 3
elchi_shield_config_reload_failure_total{instance="h-shield"} 7
`
	if v, ok := parsePromGauge(body, "elchi_shield_config_reload_failures_consecutive"); !ok || v != 3 {
		t.Fatalf("want 3,true got %v,%v", v, ok)
	}
	// A longer metric sharing the prefix must NOT match the shorter name.
	if v, ok := parsePromGauge(body, "elchi_shield_config_reload_failure"); ok {
		t.Fatalf("prefix must not match a longer metric name, got %v", v)
	}
	if _, ok := parsePromGauge(body, "nope_metric"); ok {
		t.Fatal("missing metric must report not-found")
	}
}

// shieldStub serves /configz and /metrics with caller-controlled values.
type shieldStub struct {
	version  string
	empty    bool
	failures int
}

func newStubServer(t *testing.T, s *shieldStub) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/configz":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"` + s.version + `","hash":"` + s.version + `","empty":` + boolStr(s.empty) + `}`))
		case "/metrics":
			_, _ = w.Write([]byte("elchi_shield_config_reload_failures_consecutive{instance=\"h\"} " + strconv.Itoa(s.failures) + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	prev := shieldBaseURL
	shieldBaseURL = srv.URL
	return func() { shieldBaseURL = prev; srv.Close() }
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func fastReload(t *testing.T) {
	t.Helper()
	pt, pi := reloadConfirmTimeout, reloadPollInterval
	reloadConfirmTimeout, reloadPollInterval = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { reloadConfirmTimeout, reloadPollInterval = pt, pi })
}

func TestConfirmReload_VersionChangeIsSuccess(t *testing.T) {
	fastReload(t)
	// shield already holds the NEW version; before captured the OLD one.
	defer newStubServer(t, &shieldStub{version: "newhash", empty: false, failures: 0})()
	applied, ok := ConfirmReload(context.Background(), ShieldState{Version: "oldhash"}, testLogger())
	if !ok || applied != "newhash" {
		t.Fatalf("version change must confirm reload: applied=%q ok=%v", applied, ok)
	}
}

func TestConfirmReload_RejectionIsFailure(t *testing.T) {
	fastReload(t)
	// version unchanged + the failure counter advanced => rejected, kept last-good.
	defer newStubServer(t, &shieldStub{version: "oldhash", empty: false, failures: 1})()
	applied, ok := ConfirmReload(context.Background(), ShieldState{Version: "oldhash", Failures: 0}, testLogger())
	if ok || applied != "oldhash" {
		t.Fatalf("a rejected reload must report ok=false + last-good version: applied=%q ok=%v", applied, ok)
	}
}

func TestConfirmReload_IdenticalIsSuccess(t *testing.T) {
	fastReload(t)
	// version never moves and no new failure => identical re-push, nothing to reload.
	defer newStubServer(t, &shieldStub{version: "samehash", empty: false, failures: 0})()
	applied, ok := ConfirmReload(context.Background(), ShieldState{Version: "samehash", Failures: 0}, testLogger())
	if !ok || applied != "samehash" {
		t.Fatalf("identical re-push must be a success: applied=%q ok=%v", applied, ok)
	}
}

func TestConfirmReload_UnreachableIsFailure(t *testing.T) {
	fastReload(t)
	// Point at a closed port: shield unreachable => cannot confirm => ok=false.
	prev := shieldBaseURL
	shieldBaseURL = "http://127.0.0.1:1" // unroutable/closed
	defer func() { shieldBaseURL = prev }()
	applied, ok := ConfirmReload(context.Background(), ShieldState{Version: "x", Reachable: false}, testLogger())
	if ok {
		t.Fatalf("an unreachable shield must not confirm a reload: applied=%q ok=%v", applied, ok)
	}
}

func TestSnapshotState_ReadsVersionAndFailures(t *testing.T) {
	defer newStubServer(t, &shieldStub{version: "v1", empty: false, failures: 2})()
	st := SnapshotState(context.Background())
	if st.Version != "v1" || !st.Reachable || st.Failures != 2 {
		t.Fatalf("snapshot mismatch: %+v", st)
	}
}
