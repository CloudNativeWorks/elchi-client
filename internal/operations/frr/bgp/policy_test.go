package bgp

import (
	"strings"
	"testing"

	"github.com/CloudNativeWorks/elchi-proto/client"
)

// buildPrefixListLine, generatePrefixListCommands and
// generateRemoveCommunityListSeqCommands are pure (they touch no vtysh/logger
// fields), so a zero-value PolicyManager is enough to exercise them.

func TestBuildPrefixListLine_ActionFormatting(t *testing.T) {
	pm := &PolicyManager{}

	cases := []struct {
		name     string
		in       *client.BgpPrefixList
		expected string
	}{
		{
			name: "permit renders as permit, not ROUTE_MAP_PERMIT",
			in: &client.BgpPrefixList{
				Name: "TEST", Sequence: 10,
				Action: client.BgpRouteMapAction_ROUTE_MAP_PERMIT,
				Prefix: "192.168.1.0/24",
			},
			expected: "ip prefix-list TEST seq 10 permit 192.168.1.0/24",
		},
		{
			name: "deny renders as deny",
			in: &client.BgpPrefixList{
				Name: "TEST", Sequence: 20,
				Action: client.BgpRouteMapAction_ROUTE_MAP_DENY,
				Prefix: "10.0.0.0/8",
			},
			expected: "ip prefix-list TEST seq 20 deny 10.0.0.0/8",
		},
		{
			name: "ge and le both set",
			in: &client.BgpPrefixList{
				Name: "P", Sequence: 5,
				Action: client.BgpRouteMapAction_ROUTE_MAP_PERMIT,
				Prefix: "172.16.0.0/16", Ge: 24, Le: 32,
			},
			expected: "ip prefix-list P seq 5 permit 172.16.0.0/16 ge 24 le 32",
		},
		{
			name: "le only",
			in: &client.BgpPrefixList{
				Name: "P", Sequence: 6,
				Action: client.BgpRouteMapAction_ROUTE_MAP_PERMIT,
				Prefix: "172.16.0.0/16", Le: 30,
			},
			expected: "ip prefix-list P seq 6 permit 172.16.0.0/16 le 30",
		},
		{
			name: "ge only (greater than prefix length)",
			in: &client.BgpPrefixList{
				Name: "P", Sequence: 7,
				Action: client.BgpRouteMapAction_ROUTE_MAP_PERMIT,
				Prefix: "172.16.0.0/16", Ge: 24,
			},
			expected: "ip prefix-list P seq 7 permit 172.16.0.0/16 ge 24",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pm.buildPrefixListLine(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Fatalf("line mismatch:\n got: %q\nwant: %q", got, tc.expected)
			}
			// Regression guard: the enum's String() form must never leak in.
			if strings.Contains(got, "ROUTE_MAP_") {
				t.Fatalf("action enum leaked into config line: %q", got)
			}
		})
	}
}

func TestBuildPrefixListLine_ValidationErrors(t *testing.T) {
	pm := &PolicyManager{}

	t.Run("ge greater than le is rejected", func(t *testing.T) {
		_, err := pm.buildPrefixListLine(&client.BgpPrefixList{
			Name: "P", Sequence: 1,
			Action: client.BgpRouteMapAction_ROUTE_MAP_PERMIT,
			Prefix: "10.0.0.0/8", Ge: 30, Le: 24,
		})
		if err == nil {
			t.Fatal("expected error when ge > le, got nil")
		}
	})

	t.Run("ge not greater than prefix length is rejected", func(t *testing.T) {
		_, err := pm.buildPrefixListLine(&client.BgpPrefixList{
			Name: "P", Sequence: 1,
			Action: client.BgpRouteMapAction_ROUTE_MAP_PERMIT,
			Prefix: "10.0.0.0/24", Ge: 24, // ge == prefix len -> invalid
		})
		if err == nil {
			t.Fatal("expected error when ge <= prefix length, got nil")
		}
	})
}

// generatePrefixListCommands and buildPrefixListLine must stay in lockstep so
// that the line written to FRR is byte-identical to the line the idempotency
// check compares against (the original drift between them was the root bug).
func TestGeneratePrefixListCommands_MatchesBuilder(t *testing.T) {
	pm := &PolicyManager{}
	pl := &client.BgpPrefixList{
		Name: "TEST", Sequence: 10,
		Action: client.BgpRouteMapAction_ROUTE_MAP_PERMIT,
		Prefix: "192.168.1.0/24", Ge: 25, Le: 32,
	}

	cmds, err := pm.generatePrefixListCommands(pl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 3 || cmds[0] != "configure terminal" || cmds[2] != "exit" {
		t.Fatalf("unexpected command wrapper: %#v", cmds)
	}

	line, _ := pm.buildPrefixListLine(pl)
	if cmds[1] != line {
		t.Fatalf("generated command %q does not match builder %q", cmds[1], line)
	}
}

func TestGenerateRemoveCommunityListSeqCommands(t *testing.T) {
	pm := &PolicyManager{}

	t.Run("removes only the given sequence, default standard type", func(t *testing.T) {
		cmds := pm.generateRemoveCommunityListSeqCommands(&client.BgpCommunityList{
			Name: "CL", Sequence: 15,
		})
		want := "no bgp community-list standard CL seq 15"
		if len(cmds) != 3 || cmds[1] != want {
			t.Fatalf("got %#v, want middle cmd %q", cmds, want)
		}
		// Must NOT be a whole-list removal (that would drop sibling sequences).
		if !strings.Contains(cmds[1], "seq") {
			t.Fatalf("removal is not sequence-scoped: %q", cmds[1])
		}
	})

	t.Run("honors explicit type", func(t *testing.T) {
		cmds := pm.generateRemoveCommunityListSeqCommands(&client.BgpCommunityList{
			Name: "CL", Sequence: 20, Type: "expanded",
		})
		want := "no bgp community-list expanded CL seq 20"
		if cmds[1] != want {
			t.Fatalf("got %q, want %q", cmds[1], want)
		}
	})
}
