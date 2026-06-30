package network

import (
	"testing"

	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func TestRouteEntryFromClient(t *testing.T) {
	got := routeEntryFromClient(&client.Route{
		To: "10.0.0.0/24", Via: "192.168.1.1", Table: 100, Metric: 50, Scope: "global", Onlink: true,
	})
	if got.To != "10.0.0.0/24" || got.Via != "192.168.1.1" || got.Table != 100 ||
		got.Metric != 50 || got.Scope != "global" || !got.Onlink {
		t.Fatalf("unexpected entry: %+v", got)
	}

	// Zero-valued optional fields must stay zero (omitempty in YAML).
	min := routeEntryFromClient(&client.Route{To: "0.0.0.0/0", Via: "10.0.0.1"})
	if min.Table != 0 || min.Metric != 0 || min.Scope != "" {
		t.Fatalf("optional fields should be zero: %+v", min)
	}
}

func newRouteConfig() *NetplanRouteConfig {
	return &NetplanRouteConfig{
		Network: NetplanRouteNetwork{Version: 2, Renderer: "networkd",
			Ethernets: map[string]NetplanRouteInterface{}},
	}
}

func TestUpsertRouteEntry_AddAndDedup(t *testing.T) {
	cfg := newRouteConfig()
	e := NetplanRouteEntry{To: "10.0.0.0/24", Via: "192.168.1.1"}

	if !upsertRouteEntry(cfg, "eth0", e) {
		t.Fatal("first upsert should add (return true)")
	}
	if got := len(cfg.Network.Ethernets["eth0"].Routes); got != 1 {
		t.Fatalf("expected 1 route, got %d", got)
	}
	// Identical entry must not be duplicated.
	if upsertRouteEntry(cfg, "eth0", e) {
		t.Fatal("duplicate upsert should be a no-op (return false)")
	}
	if got := len(cfg.Network.Ethernets["eth0"].Routes); got != 1 {
		t.Fatalf("expected still 1 route, got %d", got)
	}
}

// REPLACE must drop the stale entry to the same destination and install the new
// one — the bug was that REPLACE never touched the netplan file at all.
func TestRemoveRoutesToDestination_ThenUpsert_ReplacesByDestination(t *testing.T) {
	cfg := newRouteConfig()
	// Existing persisted route: dest 10.0.0.0/24 via OLD gateway.
	upsertRouteEntry(cfg, "eth0", NetplanRouteEntry{To: "10.0.0.0/24", Via: "192.168.1.1", Metric: 100})
	// An unrelated route on the same interface must survive.
	upsertRouteEntry(cfg, "eth0", NetplanRouteEntry{To: "172.16.0.0/16", Via: "192.168.1.254"})

	// Replace dest 10.0.0.0/24 with a NEW gateway/metric.
	if !removeRoutesToDestination(cfg, "eth0", "10.0.0.0/24") {
		t.Fatal("expected removal of the stale destination entry")
	}
	upsertRouteEntry(cfg, "eth0", NetplanRouteEntry{To: "10.0.0.0/24", Via: "10.9.9.9", Metric: 5})

	routes := cfg.Network.Ethernets["eth0"].Routes
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes (unrelated + replaced), got %d: %+v", len(routes), routes)
	}

	var replaced, unrelated bool
	for _, r := range routes {
		if r.To == "10.0.0.0/24" {
			if r.Via != "10.9.9.9" || r.Metric != 5 {
				t.Fatalf("destination not replaced with new values: %+v", r)
			}
			replaced = true
		}
		if r.To == "172.16.0.0/16" {
			unrelated = true
		}
	}
	if !replaced {
		t.Fatal("replaced route missing")
	}
	if !unrelated {
		t.Fatal("unrelated route was wrongly removed")
	}
}

// DOCUMENTED BEHAVIOUR: REPLACE keys on the destination only, so when several
// routes share the same destination (e.g. ECMP / failover with different gateways
// or metrics), a REPLACE of that destination collapses ALL of them into the single
// new entry. This is intentional given the current proto (a Route REPLACE carries
// no old-identity to target one specific sibling), but it MUST stay a conscious,
// tested choice: if the control-plane model ever allows multiple live routes to one
// destination, REPLACE has to key on more than To (e.g. To+Via) instead.
func TestRemoveRoutesToDestination_CollapsesMultipleSameDestination(t *testing.T) {
	cfg := newRouteConfig()
	// Two routes to the same destination (failover via different gateways).
	upsertRouteEntry(cfg, "eth0", NetplanRouteEntry{To: "10.0.0.0/24", Via: "192.168.1.1", Metric: 100})
	upsertRouteEntry(cfg, "eth0", NetplanRouteEntry{To: "10.0.0.0/24", Via: "192.168.1.2", Metric: 200})

	if !removeRoutesToDestination(cfg, "eth0", "10.0.0.0/24") {
		t.Fatal("expected removal of the destination entries")
	}
	upsertRouteEntry(cfg, "eth0", NetplanRouteEntry{To: "10.0.0.0/24", Via: "10.9.9.9", Metric: 5})

	routes := cfg.Network.Ethernets["eth0"].Routes
	if len(routes) != 1 {
		t.Fatalf("REPLACE-by-destination should collapse both siblings into one, got %d: %+v", len(routes), routes)
	}
	if routes[0].Via != "10.9.9.9" {
		t.Fatalf("surviving route should be the replacement, got %+v", routes[0])
	}
}

func TestRemoveRoutesToDestination_NoMatch(t *testing.T) {
	cfg := newRouteConfig()
	upsertRouteEntry(cfg, "eth0", NetplanRouteEntry{To: "10.0.0.0/24", Via: "1.1.1.1"})
	if removeRoutesToDestination(cfg, "eth0", "8.8.8.0/24") {
		t.Fatal("removal of absent destination should return false")
	}
	if removeRoutesToDestination(cfg, "missingIface", "10.0.0.0/24") {
		t.Fatal("removal on absent interface should return false")
	}
}
