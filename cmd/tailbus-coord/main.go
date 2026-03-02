package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alexanderfrey/tailbus/internal/config"
	"github.com/alexanderfrey/tailbus/internal/coord"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	listenAddr := flag.String("listen", ":8443", "listen address")
	dataDir := flag.String("data-dir", "", "data directory")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var cfg config.CoordConfig
	if *configPath != "" {
		loaded, err := config.LoadCoordConfig(*configPath)
		if err != nil {
			logger.Error("failed to load config", "error", err)
			os.Exit(1)
		}
		cfg = *loaded
	} else {
		cfg.ListenAddr = *listenAddr
		cfg.DataDir = *dataDir
	}

	if cfg.DataDir == "" {
		cfg.DataDir = "."
	}

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		logger.Error("failed to create data dir", "error", err)
		os.Exit(1)
	}

	store, err := coord.NewStore(filepath.Join(cfg.DataDir, "coord.db"))
	if err != nil {
		logger.Error("failed to open store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	srv := coord.NewServer(store, logger)

	// Start stale-node reaper (90s TTL, 30s sweep)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartReaper(ctx, 90*time.Second, 30*time.Second)

	lis, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		logger.Error("failed to listen", "error", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting down")
		cancel()
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}
