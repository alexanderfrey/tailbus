package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// CoordConfig is the configuration for the coordination server.
type CoordConfig struct {
	ListenAddr string `toml:"listen_addr"`
	DataDir    string `toml:"data_dir"`
}

// DaemonConfig is the configuration for a node daemon.
type DaemonConfig struct {
	NodeID        string `toml:"node_id"`
	CoordAddr     string `toml:"coord_addr"`
	AdvertiseAddr string `toml:"advertise_addr"`
	ListenAddr    string `toml:"listen_addr"`
	SocketPath    string `toml:"socket_path"`
	KeyFile       string `toml:"key_file"`
	DataDir       string `toml:"data_dir"`
}

// LoadCoordConfig loads a coordination server config from a TOML file.
func LoadCoordConfig(path string) (*CoordConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg CoordConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8443"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/tailbus-coord"
	}
	return &cfg, nil
}

// LoadDaemonConfig loads a daemon config from a TOML file.
func LoadDaemonConfig(path string) (*DaemonConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg DaemonConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = "/var/run/tailbus/tailbusd.sock"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":9443"
	}
	if cfg.KeyFile == "" {
		cfg.KeyFile = "/var/lib/tailbusd/node.key"
	}
	return &cfg, nil
}
