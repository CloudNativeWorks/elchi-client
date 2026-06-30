package cmd

import (
	"os"
	"strings"
	"sync"
	"time"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

// commandDedupTTL is how long a processed command-id is remembered. It only needs
// to cover a redelivery window (the control plane re-sending a command after a
// reconnect because it never got the response), which is seconds-to-minutes — so a
// few minutes is plenty while keeping the memory bounded and the chance of a
// spurious match against a *reused* id vanishingly small.
const commandDedupTTL = 5 * time.Minute

// commandDedupEnv disables dedup entirely when set to a falsey value. An escape
// hatch in case a deployment's control plane reuses command-ids in a way that would
// make dedup drop legitimate commands.
const commandDedupEnv = "ELCHI_COMMAND_DEDUP"

// ASSUMPTION: a CommandId uniquely identifies one logical command — a retry of the
// same command reuses the id, and two *different* commands never share an id within
// the TTL window. If a control plane violates this, set ELCHI_COMMAND_DEDUP=0.

type dedupEntry struct {
	resp      *client.CommandResponse
	expiresAt time.Time
}

// commandDeduper remembers the response to each recently-processed command-id so a
// redelivered command (same id) is answered from cache instead of being executed a
// second time. This matters for non-idempotent ops (deploy/undeploy): a reconnect
// can cause the control plane to resend a command it already delivered.
//
// It is safe for concurrent use, though in practice the command loop is serial.
type commandDeduper struct {
	mu      sync.Mutex
	entries map[string]dedupEntry
	ttl     time.Duration
	enabled bool
}

func newCommandDeduper() *commandDeduper {
	return &commandDeduper{
		entries: make(map[string]dedupEntry),
		ttl:     commandDedupTTL,
		enabled: dedupEnabled(),
	}
}

// dedupEnabled reports whether dedup is on. Default on; disabled by setting
// ELCHI_COMMAND_DEDUP to 0/false/off/no (case-insensitive).
func dedupEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(commandDedupEnv))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// dedupable reports whether a command-id participates in dedup. Empty ids and the
// synthetic "initial_connection" handshake id are excluded.
func dedupable(commandID string) bool {
	return commandID != "" && commandID != "initial_connection"
}

// get returns the cached response for id if one is present and unexpired. The
// caller must refresh the response's Identity before resending (the session token
// may have changed across a reconnect). nowFn is injectable for tests.
func (d *commandDeduper) get(id string, now time.Time) (*client.CommandResponse, bool) {
	if !d.enabled || !dedupable(id) {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.entries[id]
	if !ok || now.After(e.expiresAt) {
		return nil, false
	}
	return e.resp, true
}

// remember caches resp under id and opportunistically evicts expired entries.
func (d *commandDeduper) remember(id string, resp *client.CommandResponse, now time.Time) {
	if !d.enabled || !dedupable(id) || resp == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, e := range d.entries {
		if now.After(e.expiresAt) {
			delete(d.entries, k)
		}
	}
	d.entries[id] = dedupEntry{resp: resp, expiresAt: now.Add(d.ttl)}
}
