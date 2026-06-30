package services

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/CloudNativeWorks/elchi-client/internal/cmdrunner"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/filebeat"
	"github.com/CloudNativeWorks/elchi-client/internal/operations/rsyslog"
	"github.com/CloudNativeWorks/elchi-client/pkg/helper"
	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	client "github.com/CloudNativeWorks/elchi-proto/client"
	"google.golang.org/protobuf/proto"
)

// ReconcileInterval is the default self-heal cadence. Override with the
// ELCHI_RECONCILE_INTERVAL env var (any Go duration, e.g. "30s", "2m"); a value
// <= 0 disables the reconcile loop entirely.
const ReconcileInterval = 1 * time.Minute

// reconcileIntervalEnv names the env var that overrides ReconcileInterval.
const reconcileIntervalEnv = "ELCHI_RECONCILE_INTERVAL"

const (
	rsyslogDesiredFile  = "rsyslog.pb"
	filebeatDesiredFile = "filebeat.pb"
)

// resolveReconcileInterval reads the interval override from the environment. It
// returns the default when unset, and an error string (for logging) plus the
// default when the value is malformed. A parsed value <= 0 is returned as-is and
// means "disabled".
func resolveReconcileInterval() (time.Duration, string) {
	raw := os.Getenv(reconcileIntervalEnv)
	if raw == "" {
		return ReconcileInterval, ""
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return ReconcileInterval, fmt.Sprintf("invalid %s=%q (%v), using default %s", reconcileIntervalEnv, raw, err, ReconcileInterval)
	}
	return d, ""
}

// Reconciler periodically repairs manually-deleted or drifted rsyslog/filebeat
// config by re-asserting the last-known-desired config the control plane delivered.
//
// It reconciles ONLY toward a state the control plane actually delivered: if no
// config was ever pushed, it does nothing — it never invents config — so it cannot
// fight the control plane. The repair reuses the normal UpdateConfig path (atomic,
// validated write + service restart), and every repair is logged.
type Reconciler struct {
	logger *logger.Logger
	runner *cmdrunner.CommandsRunner
	// lastFailure dedupes repeated re-apply failures so a persistently-unappliable
	// config logged once does not spam the log every tick. Keyed by subsystem; the
	// entry is cleared on success or when the failure text changes.
	lastFailure map[string]string
}

// NewReconciler builds a reconciler with its own command runner.
func NewReconciler(baseLogger *logger.Logger) *Reconciler {
	return &Reconciler{
		logger:      baseLogger,
		runner:      cmdrunner.NewCommandsRunner(),
		lastFailure: make(map[string]string),
	}
}

// reportFailure logs a re-apply failure only the first time it is seen (or when its
// text changes), so a config that keeps failing every tick is not logged on repeat.
func (r *Reconciler) reportFailure(subsystem, msg string) {
	if r.lastFailure[subsystem] == msg {
		return // already reported this exact failure; stay quiet until it changes
	}
	r.lastFailure[subsystem] = msg
	r.logger.Errorf("%s", msg)
}

// clearFailure resets the dedupe state for a subsystem after a clean pass, so a
// future failure is logged again.
func (r *Reconciler) clearFailure(subsystem string) {
	delete(r.lastFailure, subsystem)
}

// Start runs the reconcile loop until ctx is cancelled.
func (r *Reconciler) Start(ctx context.Context) {
	defer helper.RecoverPanic(r.logger, "reconcile-loop")

	interval, warn := resolveReconcileInterval()
	if warn != "" {
		r.logger.Warn(warn)
	}
	if interval <= 0 {
		r.logger.Infof("Config reconcile loop disabled via %s", reconcileIntervalEnv)
		return
	}

	r.logger.Infof("Config reconcile loop started (interval %s)", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Debug("Config reconcile loop stopped")
			return
		case <-ticker.C:
			r.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce runs a single reconcile pass over every managed subsystem. Each is
// independent and best-effort: a failure in one is logged and never blocks another.
func (r *Reconciler) reconcileOnce(ctx context.Context) {
	r.reconcileRsyslog(ctx)
	r.reconcileFilebeat(ctx)
	r.reconcileLogrotate(ctx)
}

// needsReassert is the pure reconcile decision: re-apply only when the control
// plane has delivered a desired state AND the live file is either missing or no
// longer matches it. With no desired state we never touch anything.
func needsReassert(hasDesired, liveExists, liveMatches bool) bool {
	if !hasDesired {
		return false
	}
	return !liveExists || !liveMatches
}

func (r *Reconciler) reconcileRsyslog(ctx context.Context) {
	desired, hasDesired, err := loadRsyslogDesired()
	if err != nil {
		r.logger.Warnf("reconcile rsyslog: could not load desired state: %v", err)
		return
	}
	if !hasDesired {
		return
	}

	want, err := rsyslog.RenderConfig(desired)
	if err != nil {
		// A persisted config that won't render repeats every tick — dedupe the log.
		r.reportFailure("rsyslog", fmt.Sprintf("reconcile rsyslog: could not render desired config: %v", err))
		return
	}

	live, liveExists, err := readLiveFile(rsyslog.ConfigPath)
	if err != nil {
		r.logger.Warnf("reconcile rsyslog: could not read live config: %v", err)
		return
	}

	if !needsReassert(true, liveExists, bytes.Equal(live, []byte(want))) {
		r.clearFailure("rsyslog") // in sync — reset dedupe so a future failure logs again
		return
	}

	if liveExists {
		r.logger.Warnf("reconcile rsyslog: %s drifted from last-known-desired, re-asserting", rsyslog.ConfigPath)
	} else {
		r.logger.Warnf("reconcile rsyslog: %s missing, recreating from last-known-desired", rsyslog.ConfigPath)
	}

	if err := rsyslog.UpdateConfig(ctx, desired, r.logger, r.runner); err != nil {
		r.reportFailure("rsyslog", fmt.Sprintf("reconcile rsyslog: re-apply failed: %v", err))
		return
	}
	r.clearFailure("rsyslog")
	r.logger.Infof("reconcile rsyslog: config repaired")
}

func (r *Reconciler) reconcileFilebeat(ctx context.Context) {
	desired, hasDesired, err := loadFilebeatDesired()
	if err != nil {
		r.logger.Warnf("reconcile filebeat: could not load desired state: %v", err)
		return
	}
	if !hasDesired {
		return
	}

	want, err := filebeat.RenderConfig(desired)
	if err != nil {
		// A persisted config that won't render repeats every tick — dedupe the log.
		r.reportFailure("filebeat", fmt.Sprintf("reconcile filebeat: could not render desired config: %v", err))
		return
	}

	live, liveExists, err := readLiveFile(filebeat.ConfigPath)
	if err != nil {
		r.logger.Warnf("reconcile filebeat: could not read live config: %v", err)
		return
	}

	if !needsReassert(true, liveExists, bytes.Equal(live, want)) {
		r.clearFailure("filebeat") // in sync — reset dedupe so a future failure logs again
		return
	}

	if liveExists {
		r.logger.Warnf("reconcile filebeat: %s drifted from last-known-desired, re-asserting", filebeat.ConfigPath)
	} else {
		r.logger.Warnf("reconcile filebeat: %s missing, recreating from last-known-desired", filebeat.ConfigPath)
	}

	if err := filebeat.UpdateConfig(ctx, desired, r.logger, r.runner); err != nil {
		r.reportFailure("filebeat", fmt.Sprintf("reconcile filebeat: re-apply failed: %v", err))
		return
	}
	r.clearFailure("filebeat")
	r.logger.Infof("reconcile filebeat: config repaired")
}

// ---- last-known-desired state persistence (under models.StateDir) ----
//
// The control plane never re-pushes config on its own, so the agent must remember
// the last config it successfully applied to be able to repair drift/deletion. The
// state files hold the marshalled proto request and may contain secrets (filebeat
// credentials), so they are written 0600 in the elchi-owned state dir (no sudo).

// PersistRsyslogDesired records the rsyslog config the control plane just applied.
func PersistRsyslogDesired(req *client.RequestRsyslog) error {
	return persistDesired(rsyslogDesiredFile, req)
}

// PersistFilebeatDesired records the filebeat config the control plane just applied.
func PersistFilebeatDesired(req *client.RequestFilebeat) error {
	return persistDesired(filebeatDesiredFile, req)
}

func persistDesired(name string, msg proto.Message) error {
	return persistDesiredIn(models.StateDir, name, msg)
}

// persistDesiredIn is persistDesired parametrized on the state dir (testable).
func persistDesiredIn(dir, name string, msg proto.Message) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal desired state: %w", err)
	}
	// Atomic write within the elchi-owned state dir (no sudo needed here).
	dst := filepath.Join(dir, name)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write desired state: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit desired state: %w", err)
	}
	return nil
}

func loadRsyslogDesired() (*client.RequestRsyslog, bool, error) {
	data, ok, err := readDesiredIn(models.StateDir, rsyslogDesiredFile)
	if err != nil || !ok {
		return nil, false, err
	}
	req := &client.RequestRsyslog{}
	if err := proto.Unmarshal(data, req); err != nil {
		return nil, false, fmt.Errorf("unmarshal desired rsyslog state: %w", err)
	}
	return req, true, nil
}

func loadFilebeatDesired() (*client.RequestFilebeat, bool, error) {
	data, ok, err := readDesiredIn(models.StateDir, filebeatDesiredFile)
	if err != nil || !ok {
		return nil, false, err
	}
	req := &client.RequestFilebeat{}
	if err := proto.Unmarshal(data, req); err != nil {
		return nil, false, fmt.Errorf("unmarshal desired filebeat state: %w", err)
	}
	return req, true, nil
}

// readDesiredIn reads a persisted state file from dir, returning exists=false (no
// error) when it has never been written.
func readDesiredIn(dir, name string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// readLiveFile reads a live config file, distinguishing "missing" (exists=false,
// no error) from a genuine read error.
func readLiveFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
