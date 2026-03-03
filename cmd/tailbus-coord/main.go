package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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

	// Load or generate coord keypair for mTLS (skip if behind edge TLS)
	var kp *identity.Keypair
	if !cfg.InsecureGRPC {
		keyFile := cfg.KeyFile
		if keyFile == "" {
			keyFile = filepath.Join(cfg.DataDir, "coord.key")
		}
		var err error
		kp, err = identity.LoadOrGenerate(keyFile)
		if err != nil {
			logger.Error("failed to load identity", "error", err)
			os.Exit(1)
		}
		logger.Info("coord identity loaded", "key_file", keyFile)
	} else {
		logger.Info("gRPC TLS disabled (insecure_grpc=true, expecting edge TLS)")
	}

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

	// Set up JWT issuer if OAuth providers are configured or JWT secret is set
	if len(cfg.OAuthProviders) > 0 || cfg.JWTSecret != "" {
		jwtIssuer, err := coord.NewJWTIssuer(cfg.DataDir, cfg.JWTSecret)
		if err != nil {
			logger.Error("failed to create JWT issuer", "error", err)
			os.Exit(1)
		}
		srv.SetJWT(jwtIssuer)
		logger.Info("JWT authentication enabled")

		// Set up OAuth device flow if providers are configured
		if len(cfg.OAuthProviders) > 0 {
			externalURL := cfg.ExternalURL
			if externalURL == "" {
				externalURL = "http://localhost" + cfg.OAuthHTTPAddr
				if cfg.OAuthHTTPAddr == "" {
					externalURL = "http://localhost:8080"
				}
			}

			var providers []coord.OAuthProviderConfig
			for _, p := range cfg.OAuthProviders {
				clientID := p.ClientID
				clientSecret := p.ClientSecret
				// Allow env var overrides for secrets (e.g. fly secrets)
				if v := os.Getenv("OAUTH_CLIENT_ID"); v != "" && clientID == "" {
					clientID = v
				}
				if v := os.Getenv("OAUTH_CLIENT_SECRET"); v != "" && clientSecret == "" {
					clientSecret = v
				}
				providers = append(providers, coord.OAuthProviderConfig{
					Name:         p.Name,
					Issuer:       p.Issuer,
					ClientID:     clientID,
					ClientSecret: clientSecret,
				})
			}

			oauthSrv, err := coord.NewOAuthServer(&coord.OAuthConfig{
				Providers:   providers,
				ExternalURL: externalURL,
			}, jwtIssuer, logger)
			if err != nil {
				logger.Error("failed to create OAuth server", "error", err)
				os.Exit(1)
			}
			srv.SetOAuth(oauthSrv)
			logger.Info("OAuth device flow enabled", "providers", len(providers))
		}
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

	// Start OAuth HTTP server if configured
	if handler := srv.HTTPHandler(); handler != nil {
		oauthAddr := cfg.OAuthHTTPAddr
		if oauthAddr == "" {
			oauthAddr = ":8080"
		}
		httpSrv := &http.Server{Addr: oauthAddr, Handler: handler}
		go func() {
			logger.Info("OAuth HTTP server listening", "addr", oauthAddr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("OAuth HTTP server error", "error", err)
			}
		}()
		go func() {
			<-ctx.Done()
			httpSrv.Close()
		}()
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
