package files

import "testing"

func TestValidateServiceName(t *testing.T) {
	valid := []string{"web-server-01", "edge_router", "envoy1", "a", "A-B_9"}
	for _, n := range valid {
		if err := ValidateServiceName(n); err != nil {
			t.Errorf("ValidateServiceName(%q) unexpected error: %v", n, err)
		}
	}

	// Path-traversal and malformed names must be rejected.
	invalid := []string{
		"",
		"../../etc/foo",
		"foo/bar",
		"foo..bar",
		".",
		"name with space",
		"foo.yaml",
		`back\slash`,
		"semi;colon",
	}
	for _, n := range invalid {
		if err := ValidateServiceName(n); err == nil {
			t.Errorf("ValidateServiceName(%q) should have returned an error", n)
		}
	}
}
