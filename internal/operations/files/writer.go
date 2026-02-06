package files

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CloudNativeWorks/elchi-client/pkg/models"
	"github.com/CloudNativeWorks/elchi-client/pkg/template"
	"github.com/CloudNativeWorks/elchi-client/pkg/tools"
	"gopkg.in/yaml.v3"
)

func WriteBootstrapFile(filename string, content []byte) (string, error) {
	var jsonObj map[string]any
	if err := json.Unmarshal(content, &jsonObj); err != nil {
		return "", fmt.Errorf("failed to unmarshal bootstrap json: %w", err)
	}
	yamlBytes, err := yaml.Marshal(jsonObj)
	if err != nil {
		return "", fmt.Errorf("failed to marshal bootstrap to yaml: %w", err)
	}
	path := filepath.Join(models.ElchiLibPath, "bootstraps", filename+".yaml")
	if err := os.WriteFile(path, yamlBytes, 0644); err != nil {
		return "", fmt.Errorf("failed to write bootstrap yaml: %w", err)
	}
	return path, nil
}

func WriteDummyNetplanFile(ifaceName, downstreamAddress string, port uint32) (string, error) {
	ipv4CIDR, err := tools.GetIPv4CIDR(downstreamAddress)
	if err != nil {
		return "", fmt.Errorf("invalid IP address format: %w", err)
	}

	networkContent := fmt.Sprintf(template.DummyNetPlan, ifaceName, ipv4CIDR)
	networkPath := filepath.Join(models.NetplanPath, fmt.Sprintf("90-%s.yaml", ifaceName))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sudo", "tee", networkPath)
	cmd.Stdin = strings.NewReader(networkContent)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to write network file: %w", err)
	}

	// Set correct permissions
	if err := exec.CommandContext(ctx, "sudo", "chmod", "0600", networkPath).Run(); err != nil {
		return "", fmt.Errorf("failed to set file permissions: %w", err)
	}

	return networkPath, nil
}

func WriteSystemdServiceFile(filename, name, version string, port uint32) (string, error) {
	path := filepath.Join(models.SystemdPath, filename+".service")
	content := fmt.Sprintf(template.SystemdTemplate,
		name,     // Description (%s)
		version,  // ExecStartPre envoy path (%s)
		filename, // ExecStartPre bootstrap (%s)
		version,  // ExecStart envoy path (%s)
		filename, // ExecStart bootstrap (%s)
		port,     // base-id (%d)
		filename, // log-path (%s)
		filename, // SyslogIdentifier (%s)
	)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write systemd service file: %w", err)
	}
	return path, nil
}
