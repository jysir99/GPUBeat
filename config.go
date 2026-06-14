package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig `yaml:"server" json:"server"`
	Hosts  []HostConfig `yaml:"hosts" json:"hosts"`
}

type ServerConfig struct {
	Host     string         `yaml:"host" json:"host"`
	Port     int            `yaml:"port" json:"port"`
	Refresh  int            `yaml:"refresh" json:"refresh"`
	Privacy  bool           `yaml:"privacy" json:"privacy"`
	Terminal TerminalConfig `yaml:"terminal" json:"terminal"`
}

type TerminalConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Token   string `yaml:"token" json:"token,omitempty"`
}

type HostConfig struct {
	Name     string `yaml:"name" json:"name"`
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password,omitempty"`
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Region   string `yaml:"region,omitempty" json:"region,omitempty"`
	Notes    string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file failed: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file failed: %w", err)
	}
	NormalizeConfig(&cfg)
	return &cfg, nil
}

func NormalizeConfig(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 9988
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Refresh == 0 {
		cfg.Server.Refresh = 3
	}
	for i := range cfg.Hosts {
		NormalizeHostConfig(&cfg.Hosts[i])
	}
}

func NormalizeHostConfig(host *HostConfig) {
	if host.Port == 0 {
		host.Port = 22
	}
	if host.Name == "" {
		host.Name = host.Host
	}
}
