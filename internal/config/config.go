package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Project       string   `yaml:"project"`
	Repos         []string `yaml:"repos"`
	State         string   `yaml:"state"`
	DomainContext string   `yaml:"domain_context"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Project == "" {
		return nil, fmt.Errorf("config: project is required")
	}
	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("config: at least one repo is required")
	}
	if cfg.State == "" {
		cfg.State = "open"
	}
	return &cfg, nil
}
