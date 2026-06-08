package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigReadsTerminalSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte(`server:
  host: "127.0.0.1"
  port: 9999
  refresh: 5
  privacy: true
  terminal:
    enabled: true
    token: "let-me-in"
hosts:
  - name: "gpu-a"
    host: "192.0.2.20"
    username: "root"
    password: "secret"
`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Server.Terminal.Enabled {
		t.Fatal("terminal switch was not loaded")
	}
	if cfg.Server.Terminal.Token != "let-me-in" {
		t.Fatalf("terminal token = %q, want let-me-in", cfg.Server.Terminal.Token)
	}
	if cfg.Hosts[0].Port != 22 {
		t.Fatalf("default SSH port = %d, want 22", cfg.Hosts[0].Port)
	}
}
