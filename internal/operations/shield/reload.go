package shield

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
)

const (
	shieldHTTPTimeout = 2 * time.Second
	maxMetricsBytes   = 4 << 20 // cap /metrics read

	// reloadFailuresMetric is shield's consecutive-reload-failure gauge: it advances
	// when shield REJECTS a config and keeps last-good, which is exactly the
	// "rejected, not an outage" signal /configz cannot show (it keeps reporting the
	// last-good version).
	reloadFailuresMetric = "elchi_shield_config_reload_failures_consecutive"
)

// reloadConfirmTimeout bounds how long we wait for shield to pick up a pushed
// config; reloadPollInterval is the poll cadence. shield's fsnotify debounce +
// reload is sub-second, so the timeout is generous (a slow host still confirms
// rather than false-reporting a failure). Vars, not consts, so tests can shrink them.
var (
	reloadConfirmTimeout = 8 * time.Second
	reloadPollInterval   = 250 * time.Millisecond
)

// shieldHTTPClient talks to shield's loopback management endpoint.
var shieldHTTPClient = &http.Client{Timeout: shieldHTTPTimeout}

// shieldBaseURL is shield's management base URL. A package var (not a const built
// inline) so tests can point it at an httptest server.
var shieldBaseURL = "http://" + models.ShieldHTTPAddr

// configz mirrors the subset of elchi-shield's /configz JSON we need. shield's
// version is a CONTENT HASH of the merged config (not the bundle's opaque
// ShieldConfig.Version), so a reload is confirmed by the version CHANGING, not by
// matching the pushed bundle version.
type configz struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
	Empty   bool   `json:"empty"`
}

// ShieldState is a snapshot of shield's active-config view captured before a push,
// so ConfirmReload can distinguish "loaded the new config" (version changed) from
// "rejected, kept last-good" (failure counter advanced).
type ShieldState struct {
	Version   string
	Empty     bool
	Failures  float64
	Reachable bool
}

// SnapshotState reads shield's current active version + reload-failure counter.
// Best-effort: an unreachable shield yields a zero-value (Reachable=false) state,
// which ConfirmReload still handles.
func SnapshotState(ctx context.Context) ShieldState {
	st := ShieldState{}
	c, err := readConfigz(ctx)
	if err != nil {
		// shield unreachable — skip the metrics read (it would only time out too).
		return st
	}
	st.Version = c.Version
	st.Empty = c.Empty
	st.Reachable = true
	if f, ok := readReloadFailures(ctx); ok {
		st.Failures = f
	}
	return st
}

// ConfirmReload polls shield after a push and reports the truthful active version +
// whether the new config actually loaded. It returns ok=true when shield's version
// advances (new config live) or when nothing changed because the pushed content was
// identical to what's already loaded; ok=false when shield's reload-failure counter
// advances (rejected → kept last-good) or shield's state cannot be confirmed.
func ConfirmReload(ctx context.Context, before ShieldState, log *logger.Logger) (appliedVersion string, reloadOk bool) {
	deadline := time.Now().Add(reloadConfirmTimeout)
	var last configz
	haveLast := false

	for {
		if c, err := readConfigz(ctx); err == nil {
			last, haveLast = c, true
			if !c.Empty && c.Version != before.Version {
				return c.Version, true // new config is live
			}
		}
		if f, ok := readReloadFailures(ctx); ok && f > before.Failures {
			log.Warnf("shield rejected config (reload-failure counter %v -> %v); kept last-good", before.Failures, f)
			return before.Version, false
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return before.Version, false
		case <-time.After(reloadPollInterval):
		}
	}

	// Timed out with no version change and no new failure.
	if haveLast && last.Version != "" && !last.Empty {
		// shield is reachable and holds a config whose version did not move →
		// the pushed content was identical to what's already loaded (idempotent
		// re-push); nothing to reload, so this is a success.
		return last.Version, true
	}
	// Shield's state could not be confirmed (HTTP unreachable, or it reports no
	// config). Report unconfirmed rather than claim a successful rollout.
	log.Debugf("shield reload not confirmed (reachable_before=%v, last_version=%q)", before.Reachable, last.Version)
	return before.Version, false
}

func readConfigz(ctx context.Context) (configz, error) {
	body, err := shieldGet(ctx, "/configz")
	if err != nil {
		return configz{}, err
	}
	var c configz
	if err := json.Unmarshal(body, &c); err != nil {
		return configz{}, fmt.Errorf("parse /configz: %w", err)
	}
	return c, nil
}

func readReloadFailures(ctx context.Context) (float64, bool) {
	body, err := shieldGet(ctx, "/metrics")
	if err != nil {
		return 0, false
	}
	return parsePromGauge(string(body), reloadFailuresMetric)
}

func shieldGet(ctx context.Context, path string) ([]byte, error) {
	url := shieldBaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := shieldHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("shield %s: status %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxMetricsBytes))
}

// parsePromGauge extracts a single-series gauge value from Prometheus exposition
// text. It matches the exact metric name (ignoring labels) and returns the trailing
// value of the first matching sample line.
func parsePromGauge(body, name string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if line == "" || line[0] == '#' || !strings.HasPrefix(line, name) {
			continue
		}
		// Ensure an exact metric match: the char after the name must start the
		// label set or the value (guards against a longer metric with this prefix).
		rest := line[len(name):]
		if rest == "" || (rest[0] != '{' && rest[0] != ' ') {
			continue
		}
		sp := strings.LastIndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(line[sp+1:]), 64); err == nil {
			return v, true
		}
	}
	return 0, false
}
