package files

import "fmt"

// ValidateServiceName rejects names that could escape the intended directories
// when joined into file paths (path traversal via "/" or "..") or are otherwise
// malformed. The allowed set mirrors the deploy-time validation: letters,
// digits, '-' and '_'. Undeploy did not validate the control-plane-supplied
// name before filepath.Join'ing it into os.Remove targets, so a name like
// "../../etc/foo" could delete files outside the managed directories.
func ValidateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("service name is empty")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return fmt.Errorf("invalid service name %q: only letters, digits, '-' and '_' are allowed", name)
		}
	}
	return nil
}
