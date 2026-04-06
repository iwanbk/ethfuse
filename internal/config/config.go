package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds the application configuration.
type Config struct {
	RPCURL   string `toml:"rpc_url"`
	LogLevel string `toml:"log_level"`
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	return &Config{
		RPCURL:   "",
		LogLevel: "info",
	}
}

// Load loads configuration with the following priority:
// 1. CLI flags (passed as parameters)
// 2. Environment variables (ETHFUSE_RPC_URL)
// 3. Config file (~/.config/ethfuse/config.toml)
// 4. Default values
//
// rpcURLFlag is the value from the -rpc-url CLI flag (empty if not provided).
func Load(rpcURLFlag string) (*Config, error) {
	cfg := DefaultConfig()

	// Load from config file first (lowest priority after defaults)
	configFile := getConfigPath()
	if _, err := os.Stat(configFile); err == nil {
		if _, err := toml.DecodeFile(configFile, cfg); err != nil {
			return nil, err
		}
	}

	// Environment variable overrides config file
	if envRPC := os.Getenv("ETHFUSE_RPC_URL"); envRPC != "" {
		cfg.RPCURL = envRPC
	}

	// CLI flag has highest priority
	if rpcURLFlag != "" {
		cfg.RPCURL = rpcURLFlag
	}

	return cfg, nil
}

// getConfigPath returns the path to the config file.
func getConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home dir is not available
		return "config.toml"
	}
	return filepath.Join(home, ".config", "ethfuse", "config.toml")
}
