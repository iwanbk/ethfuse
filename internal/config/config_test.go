package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultConfig(t *testing.T) {
	os.Unsetenv("ETHFUSE_RPC_URL")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.RPCURL != "" {
		t.Errorf("RPCURL = %q, want empty string", cfg.RPCURL)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want \"info\"", cfg.LogLevel)
	}
}

func TestLoad_ConfigFile(t *testing.T) {
	os.Unsetenv("ETHFUSE_RPC_URL")

	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".config", "ethfuse", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("Failed to create config directory: %v", err)
	}

	configContent := `rpc_url = "https://example.com/rpc"
log_level = "debug"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Temporarily override home directory
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.RPCURL != "https://example.com/rpc" {
		t.Errorf("RPCURL = %q, want \"https://example.com/rpc\"", cfg.RPCURL)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want \"debug\"", cfg.LogLevel)
	}
}

func TestLoad_EnvVarOverridesConfigFile(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".config", "ethfuse", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("Failed to create config directory: %v", err)
	}

	configContent := `rpc_url = "https://example.com/rpc"
log_level = "debug"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Temporarily override home directory and env var
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	os.Setenv("ETHFUSE_RPC_URL", "https://env-override.com/rpc")
	defer os.Unsetenv("ETHFUSE_RPC_URL")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.RPCURL != "https://env-override.com/rpc" {
		t.Errorf("RPCURL = %q, want \"https://env-override.com/rpc\"", cfg.RPCURL)
	}
	// LogLevel should still come from config file
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want \"debug\"", cfg.LogLevel)
	}
}

func TestLoad_CLIFlagOverridesAll(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".config", "ethfuse", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("Failed to create config directory: %v", err)
	}

	configContent := `rpc_url = "https://example.com/rpc"
log_level = "debug"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Temporarily override home directory and env var
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	os.Setenv("ETHFUSE_RPC_URL", "https://env-override.com/rpc")
	defer os.Unsetenv("ETHFUSE_RPC_URL")

	cfg, err := Load("https://cli-flag.com/rpc")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.RPCURL != "https://cli-flag.com/rpc" {
		t.Errorf("RPCURL = %q, want \"https://cli-flag.com/rpc\"", cfg.RPCURL)
	}
}
