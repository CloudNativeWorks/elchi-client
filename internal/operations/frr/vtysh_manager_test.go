package frr

import "testing"

func TestFindVtyshConfigError(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		// vtysh prints these to stdout while exiting 0 — they MUST be detected.
		{"invalid input", "% Invalid input detected at '^' marker.", "% Invalid"},
		{"incomplete", "% Incomplete command.", "% Incomplete"},
		{"ambiguous", "% Ambiguous command.", "% Ambiguous"},
		{"malformed", "% Malformed address", "% Malformed"},
		{"unknown command", "% Unknown command: foobar", "% Unknown command"},
		{
			name:   "error buried in a multi-line batch",
			output: "line ok\nline ok\n% Invalid input detected\nmore",
			want:   "% Invalid",
		},

		// These must NOT be treated as errors.
		{"empty output", "", ""},
		{"clean success", "Configuration saved.", ""},
		// Idempotent "no ..." of an absent object: FRR answers "% Can't find ...".
		// Matching it would break cleanup flows, so it must return "".
		{"cant find prefix-list", "% Can't find specified prefix-list", ""},
		{"cant find community-list", "% Can't find community-list", ""},
		{"does not exist", "% Route-map foo does not exist", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findVtyshConfigError(tc.output)
			if got != tc.want {
				t.Fatalf("findVtyshConfigError(%q) = %q, want %q", tc.output, got, tc.want)
			}
		})
	}
}
