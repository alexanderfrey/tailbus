package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCoordConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coord.toml")
	content := `
listen_addr = ":9000"
data_dir = "/tmp/coord"
`
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadCoordConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9000" {
		t.Errorf("listen_addr = %q", cfg.ListenAddr)
	}
	if cfg.DataDir != "/tmp/coord" {
		t.Errorf("data_dir = %q", cfg.DataDir)
	}
}

func TestLoadCoordConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coord.toml")
	os.WriteFile(path, []byte(""), 0644)

	cfg, err := LoadCoordConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8443" {
		t.Errorf("default listen_addr = %q, want :8443", cfg.ListenAddr)
	}
}

func TestLoadDaemonConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	content := `
node_id = "node-1"
coord_addr = "10.0.0.1:8443"
advertise_addr = "10.0.0.2:9443"
listen_addr = ":9443"
socket_path = "/tmp/tailbus.sock"
key_file = "/tmp/node.key"
`
	os.WriteFile(path, []byte(content), 0644)

	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "node-1" {
		t.Errorf("node_id = %q", cfg.NodeID)
	}
	if cfg.CoordAddr != "10.0.0.1:8443" {
		t.Errorf("coord_addr = %q", cfg.CoordAddr)
	}
}
