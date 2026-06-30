package services

import (
	"context"
	"io"
	"os"
	"strings"
)

// Install-time log-rotation artifacts. These are STATIC (no per-host templating)
// and are created by elchi-install.sh. Nothing recreated them at runtime, so if an
// operator deleted one, elchi's envoy logs would stop rotating and could fill the
// disk. The reconcile loop recreates them when MISSING — deliberately NOT on drift,
// so a future installer tweak to these files is never fought by the agent.
//
// The contents below MUST stay byte-identical to elchi-install.sh.
const (
	logrotateConfigPath = "/etc/logrotate.d/elchi"
	logrotateScriptPath = "/usr/local/bin/logrotate-5min.sh"
	logrotateCronPath   = "/etc/cron.d/logrotate-5min"

	logrotateConfigContent = `/var/log/elchi/*.log {
    size 200M
    rotate 5
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    sharedscripts
    postrotate
        pidof envoy | xargs -r kill -SIGUSR1 2>/dev/null || true
    endscript
}
`
	logrotateScriptContent = `#!/bin/bash
/usr/sbin/logrotate /etc/logrotate.d/elchi
`
	logrotateCronContent = `*/5 * * * * root /usr/local/bin/logrotate-5min.sh
`
)

// logrotateArtifact is one static file the reconcile loop ensures exists.
type logrotateArtifact struct {
	path    string
	mode    string // chmod argument
	content string
}

func logrotateArtifacts() []logrotateArtifact {
	return []logrotateArtifact{
		{path: logrotateConfigPath, mode: "644", content: logrotateConfigContent},
		{path: logrotateScriptPath, mode: "755", content: logrotateScriptContent},
		{path: logrotateCronPath, mode: "644", content: logrotateCronContent},
	}
}

// reconcileLogrotate recreates any missing log-rotation artifact. It only acts on a
// genuinely-absent file (os.Stat ENOENT); an existing file is left untouched so the
// agent never fights a manual or installer-driven edit. A stat error other than
// "not exist" (e.g. permission) is logged and skipped, never treated as missing.
func (r *Reconciler) reconcileLogrotate(ctx context.Context) {
	for _, a := range logrotateArtifacts() {
		if _, err := os.Stat(a.path); err == nil {
			continue // present — leave it alone
		} else if !os.IsNotExist(err) {
			r.logger.Warnf("reconcile logrotate: cannot stat %s: %v", a.path, err)
			continue
		}

		r.logger.Warnf("reconcile logrotate: %s missing, recreating", a.path)
		if err := r.writeRootFile(ctx, a.path, a.content, a.mode); err != nil {
			r.reportFailure("logrotate:"+a.path, "reconcile logrotate: failed to recreate "+a.path+": "+err.Error())
			continue
		}
		r.clearFailure("logrotate:" + a.path)
		r.logger.Infof("reconcile logrotate: recreated %s", a.path)
	}
}

// writeRootFile creates a root-owned file via sudo (tee + chmod). It is used only
// for recreating a MISSING file, so a non-atomic write is fine: there is no live
// content to protect, and a partial write would be repaired on the next tick.
func (r *Reconciler) writeRootFile(ctx context.Context, path, content, mode string) error {
	tee := r.runner.SetCommandWithS(ctx, "tee", path)
	tee.Stdin = strings.NewReader(content)
	tee.Stdout = io.Discard
	if err := tee.Run(); err != nil {
		return err
	}
	return r.runner.RunWithS(ctx, "chmod", mode, path)
}
