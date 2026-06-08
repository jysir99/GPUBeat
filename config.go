package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Hosts  []HostConfig `yaml:"hosts"`
}

type ServerConfig struct {
	Host     string         `yaml:"host"`
	Port     int            `yaml:"port"`
	Refresh  int            `yaml:"refresh"`
	Privacy  bool           `yaml:"privacy"`
	Terminal TerminalConfig `yaml:"terminal"`
}

type TerminalConfig struct {
	Enabled bool `yaml:"enabled"`
}

type HostConfig struct {
	Name     string `yaml:"name"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
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
		if cfg.Hosts[i].Port == 0 {
			cfg.Hosts[i].Port = 22
		}
		if cfg.Hosts[i].Name == "" {
			cfg.Hosts[i].Name = cfg.Hosts[i].Host
		}
	}
	return &cfg, nil
}
