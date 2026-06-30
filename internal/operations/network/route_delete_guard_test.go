package network

import (
	"strings"
	"testing"

	"github.com/CloudNativeWorks/elchi-client/pkg/logger"
	client "github.com/CloudNativeWorks/elchi-proto/client"
)

func newTestRouteManager(t *testing.T) *RouteManagerNew {
	t.Helper()
	if err := logger.Init(logger.Config{Level: "error", Format: "text", Module: "test"}); err != nil {
		t.Fatalf("logger init: %v", err)
	}
	return NewRouteManagerNew(logger.NewLogger("route-test"))
}

// validateRouteDeletion is the guard that stops the agent from deleting routes that
// are dynamically/system managed (BGP, OSPF, kernel, DHCP, …). Deleting those would
// fight FRR or break connectivity, so the guard is safety-critical and must keep
// rejecting every protected protocol.
func TestValidateRouteDeletion_ProtectedProtocolsDenied(t *testing.T) {
	rm := newTestRouteManager(t)
	protected := []string{"bgp", "ospf", "isis", "zebra", "bird", "kernel", "redirect", "dhcp", "ra"}
	for _, proto := range protected {
		err := rm.validateRouteDeletion(&client.Route{Protocol: proto, To: "10.0.0.0/24"})
		if err == nil {
			t.Errorf("deletion of a %q route must be denied", proto)
			continue
		}
		if !strings.Contains(err.Error(), proto) {
			t.Errorf("error for %q should name the protocol, got: %v", proto, err)
		}
	}
}

func TestValidateRouteDeletion_UnprotectedAllowed(t *testing.T) {
	rm := newTestRouteManager(t)
	// Empty protocol (unknown) and "static" are user-managed → deletion allowed.
	for _, proto := range []string{"", "static"} {
		if err := rm.validateRouteDeletion(&client.Route{Protocol: proto, To: "10.0.0.0/24"}); err != nil {
			t.Errorf("deletion of a %q route should be allowed, got: %v", proto, err)
		}
	}
}
