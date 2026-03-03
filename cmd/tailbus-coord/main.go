package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alexanderfrey/tailbus/internal/config"
	"github.com/alexanderfrey/tailbus/internal/coord"
	"github.com/alexanderfrey/tailbus/internal/health"
	"github.com/alexanderfrey/tailbus/internal/identity"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	listenAddr := flag.String("listen", ":8443", "listen address")
	dataDir := flag.String("data-dir", "", "data directory")
	healthAddr := flag.String("health-addr", ":8080", "health endpoint listen address")
	authTokenFlag := flag.String("auth-token", "", "comma-separated pre-auth tokens for admission control")
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

	// Merge -auth-token flag into config
	if *authTokenFlag != "" {
		for _, tok := range strings.Split(*authTokenFlag, ",") {
			tok = strings.TrimSpace(tok)
			if tok != "" {
				cfg.AuthTokens = append(cfg.AuthTokens, tok)
			}
		}
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

	// Load or generate coord keypair for mTLS
	keyFile := cfg.KeyFile
	if keyFile == "" {
		keyFile = filepath.Join(cfg.DataDir, "coord.key")
	}
	kp, err := identity.LoadOrGenerate(keyFile)
	if err != nil {
		logger.Error("failed to load identity", "error", err)
		os.Exit(1)
	}
	logger.Info("coord identity loaded", "key_file", keyFile)

	srv, err := coord.NewServer(store, logger, kp)
	if err != nil {
		logger.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	// Seed auth tokens from config
	for i, tok := range cfg.AuthTokens {
		name := fmt.Sprintf("token-%d", i)
		if err := srv.Admission().SeedToken(name, tok, false); err != nil {
			logger.Error("failed to seed auth token", "name", name, "error", err)
			os.Exit(1)
		}
	}
	if len(cfg.AuthTokens) > 0 {
		logger.Info("admission control enabled", "tokens", len(cfg.AuthTokens))
	}

	// Start stale-node reaper (90s TTL, 30s sweep)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartReaper(ctx, 90*time.Second, 30*time.Second)

	lis, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		logger.Error("failed to listen", "error", err)
		os.Exit(1)
	}

	// Start health server
	if *healthAddr != "" {
		go health.Serve(ctx, *healthAddr, func() bool { return true }, logger)
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
