package config

import (
	"os"
	"path/filepath"
	"testing"
)

// LoadConfig("") is what cmd.initConfig relies on when --config is not passed.
// It must discover ./config.yaml AND unmarshal it (the bug was that the no-flag
// path read the file but never unmarshalled it, silently using defaults).
func TestLoadConfig_DefaultPathDiscoversAndUnmarshals(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // isolate $HOME/.elchi search path
	yaml := `server:
  host: "backend.example.com"
  port: 8443
  token: "secret-token-123"
  tls: true
client:
  name: "edge-01"
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig(\"\") error: %v", err)
	}

	if cfg.Server.Host != "backend.example.com" {
		t.Errorf("Host = %q, want backend.example.com (file was not unmarshalled)", cfg.Server.Host)
	}
	if cfg.Server.Port != 8443 {
		t.Errorf("Port = %d, want 8443", cfg.Server.Port)
	}
	if cfg.Server.Token != "secret-token-123" {
		t.Errorf("Token = %q, want secret-token-123", cfg.Server.Token)
	}
	if !cfg.Server.TLS {
		t.Errorf("TLS = false, want true")
	}
	if cfg.Client.Name != "edge-01" {
		t.Errorf("Client.Name = %q, want edge-01", cfg.Client.Name)
	}
}

// A missing config file must not be fatal: defaults are applied.
func TestLoadConfig_MissingFileFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig(\"\") with no file should not error, got: %v", err)
	}
	if cfg.Server.Port != 50051 {
		t.Errorf("default Port = %d, want 50051", cfg.Server.Port)
	}
}
