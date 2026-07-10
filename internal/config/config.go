package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	defaultEndpoint           = "https://kdraigo.com"
	defaultBacktesterEndpoint = "https://api.kdraigo.com"
)

type Config struct {
	Auth AuthConfig `yaml:"auth"`
	// Endpoint is the gateway base for data/analytics/frontend-api services.
	Endpoint string `yaml:"endpoint"`
	// BacktesterEndpoint is the backtester_engine base. It is a separate host
	// because the kdraigo.com gateway does not proxy the backtester (session POST
	// 405s); api.kdraigo.com forwards /api/v1/dev/* directly to the engine.
	BacktesterEndpoint string `yaml:"backtester_endpoint"`
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

	cfg := &Config{Endpoint: defaultEndpoint, BacktesterEndpoint: defaultBacktesterEndpoint}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if cfg.Auth.KeyID == "" || cfg.Auth.PrivateKey == "" {
		return nil, fmt.Errorf("%s: auth.key_id and auth.private_key are required", path)
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.BacktesterEndpoint == "" {
		cfg.BacktesterEndpoint = defaultBacktesterEndpoint
	}
	return cfg, nil
}
