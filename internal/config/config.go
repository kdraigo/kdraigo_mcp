package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Auth     AuthConfig `yaml:"auth"`
	Endpoint string     `yaml:"endpoint"`
}

type AuthConfig struct {
	KeyID      string `yaml:"key_id"`
	PrivateKey string `yaml:"private_key"`
}

func Load() (*Config, error) {
	path := os.Getenv("KDRAIGO_CONFIG")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		path = filepath.Join(home, ".kdraigo", "config.yaml")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := &Config{Endpoint: "https://kdraigo.com"}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if cfg.Auth.KeyID == "" || cfg.Auth.PrivateKey == "" {
		return nil, fmt.Errorf("%s: auth.key_id and auth.private_key are required", path)
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://kdraigo.com"
	}
	return cfg, nil
}
