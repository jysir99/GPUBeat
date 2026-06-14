package main

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type ConfigStore struct {
	mu   sync.RWMutex
	path string
	cfg  *Config
}

type HostConfigView struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Provider    string `json:"provider,omitempty"`
	Region      string `json:"region,omitempty"`
	Notes       string `json:"notes,omitempty"`
	PasswordSet bool   `json:"password_set"`
}

type HostConfigInput struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Provider string `json:"provider"`
	Region   string `json:"region"`
	Notes    string `json:"notes"`
}

type ServerConfigInput struct {
	Refresh         int     `json:"refresh"`
	Privacy         bool    `json:"privacy"`
	TerminalEnabled bool    `json:"terminal_enabled"`
	TerminalToken   *string `json:"terminal_token"`
}

type ServerConfigView struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Refresh          int    `json:"refresh"`
	Privacy          bool   `json:"privacy"`
	TerminalEnabled  bool   `json:"terminal_enabled"`
	TerminalTokenSet bool   `json:"terminal_token_set"`
}

func NewConfigStore(path string, cfg *Config) *ConfigStore {
	if cfg == nil {
		cfg = &Config{}
	}
	NormalizeConfig(cfg)
	return &ConfigStore{path: path, cfg: cloneConfig(cfg)}
}

func (s *ConfigStore) Snapshot() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.cfg)
}

func (s *ConfigStore) Server() ServerConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Server
}

func (s *ConfigStore) Terminal() TerminalConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Server.Terminal
}

func (s *ConfigStore) Hosts() []HostConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hosts := make([]HostConfig, len(s.cfg.Hosts))
	copy(hosts, s.cfg.Hosts)
	return hosts
}

func (s *ConfigStore) FindHost(name string) (HostConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, host := range s.cfg.Hosts {
		if host.Name == name {
			return host, true
		}
	}
	return HostConfig{}, false
}

func (s *ConfigStore) ConfigView() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hosts := make([]HostConfigView, 0, len(s.cfg.Hosts))
	for _, host := range s.cfg.Hosts {
		hosts = append(hosts, hostConfigView(host))
	}
	return map[string]interface{}{
		"server": serverConfigView(s.cfg.Server),
		"hosts":  hosts,
	}
}

func (s *ConfigStore) UpdateServer(input ServerConfigInput) (ServerConfigView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if input.Refresh > 0 {
		s.cfg.Server.Refresh = input.Refresh
	}
	s.cfg.Server.Privacy = input.Privacy
	s.cfg.Server.Terminal.Enabled = input.TerminalEnabled
	if input.TerminalToken != nil {
		s.cfg.Server.Terminal.Token = *input.TerminalToken
	}
	NormalizeConfig(s.cfg)
	if err := s.saveLocked(); err != nil {
		return ServerConfigView{}, err
	}
	return serverConfigView(s.cfg.Server), nil
}

func (s *ConfigStore) UpsertHost(originalName string, input HostConfigInput) (HostConfigView, bool, error) {
	host := HostConfig{
		Name:     strings.TrimSpace(input.Name),
		Host:     strings.TrimSpace(input.Host),
		Port:     input.Port,
		Username: strings.TrimSpace(input.Username),
		Password: input.Password,
		Provider: strings.TrimSpace(input.Provider),
		Region:   strings.TrimSpace(input.Region),
		Notes:    strings.TrimSpace(input.Notes),
	}
	NormalizeHostConfig(&host)
	if host.Name == "" || host.Host == "" || host.Username == "" {
		return HostConfigView{}, false, fmt.Errorf("name, host and username are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	if originalName != "" {
		for i := range s.cfg.Hosts {
			if s.cfg.Hosts[i].Name == originalName {
				idx = i
				break
			}
		}
		if idx < 0 {
			return HostConfigView{}, false, fmt.Errorf("host %q not found", originalName)
		}
	}

	for i := range s.cfg.Hosts {
		if i != idx && s.cfg.Hosts[i].Name == host.Name {
			return HostConfigView{}, false, fmt.Errorf("host name %q already exists", host.Name)
		}
	}

	created := idx < 0
	if idx >= 0 {
		if host.Password == "" {
			host.Password = s.cfg.Hosts[idx].Password
		}
		s.cfg.Hosts[idx] = host
	} else {
		s.cfg.Hosts = append(s.cfg.Hosts, host)
	}
	NormalizeConfig(s.cfg)
	if err := s.saveLocked(); err != nil {
		return HostConfigView{}, created, err
	}
	return hostConfigView(host), created, nil
}

func (s *ConfigStore) DeleteHost(name string) (HostConfigView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, host := range s.cfg.Hosts {
		if host.Name == name {
			s.cfg.Hosts = append(s.cfg.Hosts[:i], s.cfg.Hosts[i+1:]...)
			if err := s.saveLocked(); err != nil {
				return HostConfigView{}, err
			}
			return hostConfigView(host), nil
		}
	}
	return HostConfigView{}, fmt.Errorf("host %q not found", name)
}

func (s *ConfigStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := yaml.Marshal(s.cfg)
	if err != nil {
		return fmt.Errorf("marshal config failed: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0600); err != nil {
		return fmt.Errorf("write config file failed: %w", err)
	}
	return nil
}

func hostConfigView(host HostConfig) HostConfigView {
	return HostConfigView{
		Name:        host.Name,
		Host:        host.Host,
		Port:        host.Port,
		Username:    host.Username,
		Provider:    host.Provider,
		Region:      host.Region,
		Notes:       host.Notes,
		PasswordSet: host.Password != "",
	}
}

func serverConfigView(server ServerConfig) ServerConfigView {
	return ServerConfigView{
		Host:             server.Host,
		Port:             server.Port,
		Refresh:          server.Refresh,
		Privacy:          server.Privacy,
		TerminalEnabled:  server.Terminal.Enabled,
		TerminalTokenSet: server.Terminal.Token != "",
	}
}

func cloneConfig(cfg *Config) *Config {
	next := &Config{
		Server: cfg.Server,
		Hosts:  make([]HostConfig, len(cfg.Hosts)),
	}
	copy(next.Hosts, cfg.Hosts)
	return next
}
