package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAPIHidesPasswordAndRequiresTokenForWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		Server: ServerConfig{
			Terminal: TerminalConfig{Enabled: true, Token: "let-me-in"},
		},
		Hosts: []HostConfig{{
			Name:     "gpu-1",
			Host:     "192.0.2.10",
			Port:     22,
			Username: "root",
			Password: "secret",
		}},
	}
	store := NewConfigStore(path, cfg)
	handler := NewConfigHandler(store, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("GET /api/config leaked password: %s", rr.Body.String())
	}

	body := []byte(`{"name":"tencent-1","host":"203.0.113.20","port":22,"username":"ubuntu","password":"pw","provider":"Tencent Cloud","region":"ap-guangzhou"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/config/hosts", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized POST status = %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/config/hosts", bytes.NewReader(body))
	req.Header.Set("X-GPUBeat-Terminal-Token", "let-me-in")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("authorized POST status = %d body=%s", rr.Code, rr.Body.String())
	}
	var view HostConfigView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Name != "tencent-1" || !view.PasswordSet || view.Provider != "Tencent Cloud" {
		t.Fatalf("created host view = %#v", view)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tencent-1") || !strings.Contains(string(data), "ap-guangzhou") {
		t.Fatalf("config file was not updated:\n%s", string(data))
	}
}
