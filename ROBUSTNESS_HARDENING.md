# Robustness Hardening — Session Handoff

**Branch:** `robustness-hardening` (off `main`)
**Status:** **All 14 work items now done & verified on the Linux host** (build + vet + test +
gofmt + `-race`, clean except the pre-existing envoy/root test caveat). The two large items
(#8, #13) were completed on Linux; a real bug that only surfaces on Linux (#9's table guard,
see §7) was found by the now-runnable network tests and fixed. Deliberately-deferred sub-items
(§4) remain by design. Still uncommitted — commit only when the user asks.

This file is a complete handoff so a fresh Claude session can continue without re-deriving
anything. Read it top to bottom first.

---

## 0. Why this branch exists

A multi-agent audit of the `elchi-client` agent found robustness / "orphan record" / state-drift
bugs across every subsystem. The recurring root causes were:

- **A. No reconciliation anywhere** — everything is push/event-driven; manual deletion or drift is
  never repaired (this is the user's original question: "if a user deletes the rsyslog conf, does
  it recreate it?" — answer was *no*). → work item **#13** (not yet done).
- **B. "Success: true" reported even when the underlying op failed** — the control plane then never
  cleans up orphans. → largely fixed (#7, #5, #10, shield, systemd).
- **C. No rollback on partial failure** — half-applied state left behind. → fixed for deploy/upgrade
  (#5, #12); network partial-batch deferred (N-H4).

The fixes were tracked as 14 work items (#1–#14 below).

---

## 1. Environment — IMPORTANT build/verification notes

This agent is **Linux-only**: it imports `vishvananda/netlink` (Linux syscalls) and
`coreos/go-systemd/sdjournal` (cgo + libsystemd). The previous session worked on **macOS**, where:

- `internal/services`, `internal/handlers`, `cmd`, `main` **cannot be built** (journal → sdjournal → cgo+systemd).
- `internal/operations/network` and anything importing `netlink` **cannot be run** natively (compiles for linux only).

**On the Linux host you can finally build & test EVERYTHING.** Do this first to confirm the batch is green:

```bash
# one-time, if needed:
sudo apt-get install -y build-essential pkg-config libsystemd-dev

gofmt -l .                                   # must be empty
CGO_ENABLED=1 go build -tags systemd ./...   # whole tree, incl. cmd/services/handlers
go vet ./...
go test ./... 2>&1 | tail -60
```

Expected: build clean, all tests pass. **Known pre-existing exception:** `internal/operations/envoy`
tests create `/var/lib/elchi` and need root — run `sudo -E go test ./internal/operations/envoy/` or
ignore. This is **not** caused by this branch.

The macOS-only build subset the previous session used (for reference):
`CGO_ENABLED=0 GOOS=linux go build ./internal/operations/... ./pkg/... ./internal/config/ ./internal/grpc/ ./internal/initializer/`

---

## 2. Critical runtime facts discovered (do NOT re-derive; verify if changing related code)

- **The client runs as the `elchi` user, NOT root** (`elchi-install.sh` `[Service] User=$ELCHI_USER`,
  line ~1584). It is invoked as `elchi-client start --config /etc/elchi/config.yaml`.
- Because it is non-root, writes to root-owned dirs (`/etc/netplan`, `/etc/rsyslog.d`, `/etc/filebeat`,
  `/usr/lib/systemd/system`) go through **`sudo tee`** (see `internal/operations/rsyslog/manager.go`
  `UpdateConfig`, `route_new.go` `addRouteToPersistentConfig`). `cmdrunner.RunWithS` / `SetCommandWithS`
  = "run with sudo".
- **This privilege model is why several sub-items were deferred** (see §4): naive `os.Remove` /
  `os.Rename` on root-owned files will fail for the elchi user.
- `internal/operations/shield/manager.go` already does **atomic temp+rename writes** — copy that
  pattern when implementing #8.
- Heartbeat has a 30s ticker (`internal/services/heartbeat.go` ~line 266) — the intended hook for the
  reconcile loop (#13).
- Commands are dispatched **serially** in a single stream loop
  (`cmd/start.go handleCommands` → `internal/handlers/manager.go CommandManager.HandleCommand`), so
  there are currently no intra-client races on vtysh / shield conf.d / config files.

---

## 3. What was DONE (12 items) — all locally build+test+gofmt verified

Each item lists the fix and the test(s). New test files are marked `(new test)`.

| # | Sev | Files | Fix |
|---|-----|-------|-----|
| **1** | HIGH | `internal/operations/frr/bgp/policy.go` (+`policy_test.go` new) | prefix-list idempotency check rendered the `Action` enum via `%s` as `ROUTE_MAP_PERMIT` (not `permit`), so it never matched → every re-push ran `no ip prefix-list NAME` (deleting **all** sequences) then re-added one. Extracted shared `buildPrefixListLine` (used by both command-gen and the check, so they can't drift) + `updatePrefixList` now removes only `seq N`. Same per-seq fix for community-list (`generateRemoveCommunityListSeqCommands`). |
| **2** | HIGH | `internal/operations/frr/vtysh_manager.go` (+`vtysh_manager_test.go` new) | `ExecuteSimpleSession` only scanned stdout for `% Unknown command:`. vtysh prints `% Invalid/Incomplete/Ambiguous/Malformed` to stdout and exits 0, so rejected config was saved via `write memory` and reported as success. Added `findVtyshConfigError` (matches the syntax-rejection family only — NOT "Can't find", so idempotent `no` stays safe), fails **before** `WriteMemory`. |
| **3** | HIGH | `internal/operations/rsyslog/manager.go` (+`manager_test.go` new) | `GetCurrentConfig` did `strings.Split(parts[1], "\"")[1]` → index-out-of-range **panic** on a hand-edited unquoted line, killing the command stream (DoS). Added panic-safe `extractQuotedValue` + pure `parseRsyslogConfig`. |
| **4** | MED | `cmd/root.go` (+`internal/config/config_test.go` new) | Without `--config`, the code read `config.yaml` via viper but never `Unmarshal`ed it → silently used defaults (wrong host / empty token). Collapsed to `config.LoadConfig(cfgFile)` (does default-path discovery + unmarshal + logger init). Production path (`--config` always passed by the unit) unchanged. |
| **5** | HIGH | `internal/operations/upgrade/listener.go` (+`listener_test.go` new) | (a) **D-H3 per the user:** a binary upgrade MUST hard-restart (no real graceful path); removed the lie that reported "graceful restart completed" while doing an identical `SUB_RESTART`. (b) **D-H2:** verify the target binary exists *before* rewriting unit/bootstrap (else guaranteed outage); capture original unit+bootstrap bytes and **roll back** on any failure in steps 4–6, using a **fresh `context.Background()`** so a cancelled command ctx (SIGTERM) can't block rollback. |
| **6** | MED | `internal/operations/files/validate.go` (new) + `validate_test.go` (new), `internal/services/undeploy.go` | **M3 path-traversal:** undeploy passed the control-plane name straight into `filepath.Join`→`os.Remove`. Added `files.ValidateServiceName` (letters/digits/`-`/`_` only) and call it early in `UndeployService`. (M1/M2/M4 deferred — see §4.) |
| **7** | MED | `internal/operations/systemd/service.go` (+`service_test.go` new), `internal/services/shield.go`, `internal/services/rsyslog.go`, `internal/services/filebeat.go` | `ServiceControl` returned `(nil,nil)` (success+no status) when status fetch failed → now returns a non-nil "unknown" status; after start/restart/reload it verifies the unit didn't land in `failed`/`inactive` (`isFailedActiveState`, tested; only definitive bad states, so `activating` isn't false-failed). Shield `updateShieldConfig` now reports `Success = reloadOk` instead of hardcoded `true`. Removed the **double service restart** in `UpdateRsyslogConfig` and `UpdateFilebeatConfig` (`UpdateConfig` already restarts once). |
| **9** | HIGH | `internal/operations/network/route_new.go`, `policy.go`, `table.go`, `state.go` (+`route_persist_test.go`, `table_test.go` new) | **M1:** `addRoute` on `EEXIST` now still persists. **N-H1:** `replaceRoute` & `replacePolicy` now update the netplan files (were runtime-only → stale on reboot). **N-H3:** `deleteTable` now flushes kernel routes + rules in that table, guarded by `isElchiManagedTableID` so it can **never** touch system tables (main=254/local=255/default=253), tested. Removed junk fallback tables (`sadeee2`/`sadasd`). |
| **10** | HIGH | `internal/services/deployment_checker.go` | **D-H1:** `CheckExistingDeployment` only checked systemd `LoadState`, never the binary → a re-deploy after the binary was deleted reported success on a dead service. Now stats the versioned envoy binary; if missing sets `NeedsUpdate`+`ServiceNeedsRestart` so it fails honestly (the restart fails if the binary is truly gone). |
| **11** | HIGH | `internal/grpc/client.go` (+`client_test.go` new) | **C-H1:** `createConnection` reassigned `c.conn` without closing the old one → connection/FD/goroutine leak on every reconnect/stream-flap. Added `replaceConn` (atomic swap, returns the superseded conn to close), tested. |
| **12** | HIGH/MED | `internal/handlers/manager.go`, `internal/handlers/models.go`, `internal/services/deploy.go` | Central `HandleCommand` now wraps every handler in a per-command **timeout** (10m ceiling — only trips on genuine hangs) and a **panic recover** that returns a failure response instead of dropping the gRPC stream. `deploy.cleanupAndRollback` now runs on a fresh `context.Background()` so SIGTERM mid-deploy can't prevent rollback. |
| **14** | LOW | `internal/operations/common/download_utils.go` (+`download_utils_test.go` new), `internal/operations/network/state.go`, `pkg/logger/logger.go` | `MoveFile` cross-device fallback was not crash-safe (io.Copy straight into dst → truncated dst accepted as a valid binary). Now `copyAndReplace`: temp file + fsync + atomic rename, preserves mode (tested). `logger.Fatalf` no longer silently no-ops when the logger is nil (stderr + `os.Exit(1)`). |

---

## 4. DELIBERATELY DEFERRED — do NOT "fix" these blindly; each needs a decision

These were left undone on purpose. The reasons matter — a naive fix here will **break functionality**.

- **N-H2 (network/policy.go `replacePolicy` identity change):** to remove the *old* rule when a
  REPLACE changes From/To/Table (keeping Priority), the control plane must send the **old identity**.
  The proto (`client.RoutingPolicy`) does not carry it. Needs a proto/control-plane change. Code has
  a `NOTE:` comment at the spot.
- **N-H4 (network partial-batch reporting):** `ManageRoutes/ManagePolicies/ManageTableOperations`
  return on first error with no per-op result, so the control plane can't tell what actually applied.
  Fixing means an API/proto change (per-operation results) — product decision.
- **Undeploy M1/M2/M4 (`internal/services/undeploy.go`, `internal/operations/files/deleter.go`):**
  - M1 (return `Success=false` when `cleanupErrors` non-empty),
  - M2 (also delete `/var/log/elchi/<name>-<port>_{system,access}.log`),
  - M4 (remove root-owned netplan via sudo, not `os.Remove`).
  - **Why deferred:** all hinge on the **privilege model** (client is `elchi`, files are root-owned).
    If netplan/log `os.Remove` always fails for the elchi user, then turning on M1 honesty would make
    **every undeploy report failure**. On the Linux host: check who owns
    `/etc/netplan/90-elchi-if-*.yaml` and the `/var/log/elchi/*.log` files and whether the elchi user
    can remove them. Then implement M2/M4 via sudo and M1 honesty together.
- **rsyslog/filebeat `syslog.socket` honesty (`internal/operations/rsyslog/manager.go`):** socket
  failures are deliberately `Warnf`'d ("continuing anyway") and the installer stops `syslog.socket`
  as "managed via API". Making socket failures fatal may be wrong — confirm intent first.
- **#14 leftovers:** version-dir GC (needs reference counting so an in-use version isn't deleted),
  filebeat chmod-on-secrets hard-fail, IPv6 policy family (hardcoded V4), route-delete protocol guard
  via kernel lookup, unbounded `CombinedOutput` buffering, bootstrap-version `ReplaceAll` over-match
  (`upgrade/listener.go` `UpdateBootstrapVersion` rewrites *every* `value: <ver>`, not just envoy-version).
- **#12 leftovers (cmd/start.go):** command-id **dedup** for redelivery; collapsing the **two
  reconnect loops** into a single authority + `Close()` on disconnect (the leak itself is already
  fixed in #11); removing the **dead** worker-pool/rate-limiter/circuit-breaker created in
  `createSession` but never used.

---

## 5. THE TWO BIG ITEMS — DONE on the Linux host

### #8 — Atomic config writes + pre-flight validation (rsyslog/filebeat)  [MED]  ✅ DONE

**What was done:**
- New `internal/operations/common/atomic_write.go`:
  - `TempSiblingPath(dst)` → hidden sibling temp (`.<base>.elchi-tmp`); the leading dot + non-`.conf`
    suffix keeps rsyslog's `*.conf` wildcard from loading it pre-commit. Pure, unit-tested.
  - `AtomicReplaceFileWithS(...)` → `sudo tee <tmp>` → `sudo chmod <mode> <tmp>` → `validate(tmp)` →
    `sudo mv -f <tmp> <dst>` (atomic same-fs rename). Live file is untouched until validation passes;
    temp is removed on any failure. Mirrors shield's temp+rename spirit.
  - `ClassifyValidatorResult(exitErr, output)` → `ConfigValid | ConfigInvalid | ConfigValidatorUnavailable`.
    Distinguishes a genuine config rejection (block, keep old file, honest failure) from the validator
    not being able to run (missing binary / permission / keystore / **sudo denial**) → best-effort
    proceed, exactly like shield. Pure, table-driven, unit-tested with real rsyslogd/filebeat output.
- `rsyslog/manager.go` & `filebeat/manager.go`: `UpdateConfig` now stages+validates+atomically renames
  via the helper (`rsyslogd -N1 -f <tmp>` / `filebeat test config -c <tmp>`), then restarts. Extracted
  `RenderConfig` as the single source of truth for the rendered file (also used by #13). `UpdateConfig`
  is now guarded by a package `updateMu` so the reconcile goroutine and a control-plane UPDATE can't
  race on the shared temp path / restart.
- **Sudoers (`elchi-install.sh`)**: added the new commands to `RSYSLOG_CMDS` / `FILEBEAT_CMDS`
  (`tee`/`chmod`/`mv -f`/`rm -f` on the exact temp paths, `/usr/sbin/rsyslogd -N1 -f <tmp>`,
  `/usr/bin/filebeat test config -c <tmp>`). Verified with `visudo -cf`. Temp paths are deterministic,
  so the entries stay tightly scoped (no wildcards). **If a host has these binaries at a different path,
  sudo denies the validator → classifier treats it as "unavailable" → push still proceeds.**

### #13 — Reconcile / self-heal loop  [the user's original concern]  ✅ DONE (rsyslog + filebeat)

**What was done** (`internal/services/reconcile.go`, started from `cmd/start.go` `Run()` on `m.ctx`):
- On each successful UPDATE, `UpdateRsyslogConfig`/`UpdateFilebeatConfig` persist the applied proto
  request under `models.StateDir` = `/var/lib/elchi/state/` (`rsyslog.pb`/`filebeat.pb`, 0600 — may hold
  filebeat creds; written atomically; elchi-owned dir, no sudo). Best-effort: a persist failure never
  fails the update.
- A `Reconciler` ticks every `ReconcileInterval` (1 min). For each subsystem it loads the last-known
  desired proto, renders it via the shared `RenderConfig`, reads the live file, and re-applies through
  the normal `UpdateConfig` (atomic+validated+restart) **iff** `needsReassert(hasDesired, liveExists,
  liveMatches)` — i.e. desired was delivered AND the live file is missing or drifted. Every repair logs.
- **Does not fight the control plane:** with no delivered desired state it does nothing (never invents
  config). `needsReassert` is a pure function, unit-tested; persist/load round-trip + overwrite-wins are
  unit-tested (`reconcile_test.go`).
- **Ordering matters — persist BEFORE apply.** The handlers persist the requested config *before*
  calling `UpdateConfig`. If you persist *after* a successful apply, there is a window (the apply's
  mv + service restart, up to ~1s) where the reconcile loop sees the new live file but the *old* desired
  state and would roll the just-applied config back for a tick. Persisting first makes the invariant
  "desired is never staler than live", so reconcile only ever drives the live file forward. A persisted
  config that then fails to apply is simply retried by reconcile (the atomic writer keeps the last-good
  live file on validation failure), which is the desired self-heal behaviour. The control-plane UPDATE
  and the reconcile goroutine are additionally serialized by `updateMu` inside `UpdateConfig`.

**Deliberately left for a follow-up (NOT done — needs a decision, see §4-style reasoning):**
- **logrotate/cron/`logrotate-5min.sh` recreation:** these are *install-time* artifacts, not
  control-plane-delivered, so they don't fit the "reconcile only toward delivered desired state" model.
  Recreating them means embedding the installer's templates in Go (drift risk between installer & agent).
  Decide: own them in the installer-as-source vs. teach the agent the templates.
- **shield `full_sync` / BGP resync on reconnect:** optional; shield already self-watches its conf.d and
  the control plane re-pushes on reconnect, so lower value. Revisit if drift is observed in practice.

---

## 6. How to pick up

1. Run the §1 build/test commands. Confirm green (note the envoy/root test caveat).
2. If anything is red, fix that first (the previous session could not compile services/handlers/cmd,
   so the most likely place for a surprise is a services-layer edit — but all were gofmt/parse-checked).
3. Then implement **#8**, then **#13**, using §5. Add tests for each (the repo convention: pure logic
   extracted into testable functions; see the 11 new `_test.go` files for the style).
4. Re-run the full build+test after each item.
5. Revisit §4 deferred items only with the product/privilege decisions they require.
6. Commit / open the PR only when the user asks (previous session left everything uncommitted on
   `robustness-hardening`). PR commit-message trailer convention is in the repo guidelines.

Task list (subjects) for reference: #1 FRR prefix/community-list, #2 vtysh errors, #3 rsyslog parser,
#4 config load, #5 upgrade, #6 undeploy, #7 success-honesty, #8 atomic writes (DONE), #9 network,
#10 deploy missing-binary, #11 gRPC leak, #12 dispatch hardening, #13 reconcile (DONE), #14 low cleanup.

---

## 7. Linux-only bug found & fixed (the macOS session could not have caught this)

`internal/operations/network/table.go` — `isElchiManagedTableID` (the safety guard #9 added so
`flushTableRoutesAndRules` can never touch a kernel system table) checked only the numeric range
`100..999`. But the kernel's system tables **default=253, main=254, local=255 live INSIDE that range**,
so the guard *passed* them: a `DELETE` for table 254 would have flushed the box's **main** routing
table and taken it off the network. `TestIsElchiManagedTableID` asserted the correct behaviour but
**never ran on macOS** (netlink is Linux-only), so it failed the moment the suite ran on Linux.

**Fix:** `isElchiManagedTableID` now excludes the three system table IDs explicitly (new
`SystemTableDefault/Main/Local` consts), and the add/replace/bulk range checks were routed through the
same guard so a system-table ID can't be registered as an Elchi table either. Tests pass.

**Takeaway for future macOS→Linux handoffs:** any `_test.go` the previous session wrote for a
Linux-only package (`network`, anything importing `netlink`/`sdjournal`) was authored *blind* and is
effectively unverified until it runs here. Run the full suite first and treat every failure as a real
finding, not just the documented envoy/root caveat.
