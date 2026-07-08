package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/hubfetch"
	"github.com/mvd-analyzer/mvd-analytics/result"
	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
	"github.com/mvd-analyzer/mvd-api/internal/democache"
	"github.com/mvd-analyzer/mvd-api/internal/portal"
)

// runServe starts the HTTP REST server. Blocks until SIGINT/SIGTERM.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var (
		addr          = fs.String("addr", ":8080", "listen address")
		cacheDir      = fs.String("cache-dir", democache.DefaultRoot(), "on-disk cache root")
		cacheMaxBytes = fs.Int64("cache-max-bytes", 20<<30, "cache disk budget in bytes (tiers 1-3); background GC evicts oldest files when exceeded; 0 disables GC")
		maxParses     = fs.Int("max-parses", 0, "max concurrent heavy cold operations (demo download+parse or LOS raycast) (0 = max(1, NumCPU/2))")
		mapsDir       = fs.String("maps-dir", "", "directory of per-map geometry JSON for /v1/maps/{map}/geometry; empty disables the endpoint")
		logFormat     = fs.String("log-format", "text", "access log format: text | json")
		authDir       = fs.String("auth-dir", "", "directory holding keys.json; when set, /v1/* and POST /v1/demos/{id} require an API key. Empty = no auth (localhost mode)")
		rateUser      = fs.Float64("rate-user", 5, "per-key sustained request rate (req/s) for portal (user) keys")
		burstUser     = fs.Int("burst-user", 20, "per-key burst (bucket size) for portal (user) keys")
		rateService   = fs.Float64("rate-service", 50, "per-key sustained request rate (req/s) for service keys (e.g. mvd-web)")
		burstService  = fs.Int("burst-service", 200, "per-key burst (bucket size) for service keys")
		enablePortal  = fs.Bool("portal", false, "enable the Discord key portal at /portal (requires -auth-dir and the DISCORD_CLIENT_ID/DISCORD_CLIENT_SECRET/PORTAL_COOKIE_SECRET env vars); off = no /portal routes")
		portalBaseURL = fs.String("portal-base-url", "", "public origin for the portal, e.g. https://qw.example.com; used to build the OAuth redirect_uri and links (required with -portal)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := newLogger(*logFormat)
	cache := democache.New(*cacheDir, hubfetch.NewClient())
	cache.MaxBytes = *cacheMaxBytes
	cache.MaxParses = *maxParses
	cache.Logger = logger
	cache.CleanupOnStartup()

	// Auth is off unless -auth-dir is set. When on, the store is loaded and
	// the auth + per-key rate-limit middleware is inserted into the chain
	// (PLAN-hosting D8). Off keeps today's localhost behaviour byte-identical.
	var auth *authenticator
	if *authDir != "" {
		store, err := authkeys.Open(*authDir)
		if err != nil {
			return fmt.Errorf("auth store: %w", err)
		}
		auth = &authenticator{
			store: store,
			limiter: newKeyLimiter(
				rateClass{rate: *rateUser, burst: *burstUser},
				rateClass{rate: *rateService, burst: *burstService},
			),
			logger: logger,
		}
	}
	// Portal is off unless -portal is set. It issues into the SAME auth store
	// the middleware validates against, so it REQUIRES -auth-dir. The Discord
	// credentials and cookie HMAC secret arrive via ENV, never flags (flags
	// show in `ps` — a secret in a flag is a leak). A misconfigured portal must
	// refuse to boot, not run half-open (PLAN-hosting D5).
	var portalHandler *portal.Portal
	if *enablePortal {
		// A nil store here (no -auth-dir) is the "requires -auth-dir" config
		// error, which NewConfig rejects. Pass the interface as an explicit nil
		// (not a typed-nil *authkeys.Store, which would read as non-nil).
		var store portal.KeyStore
		if auth != nil {
			store = auth.store
		}
		cfg, err := portal.NewConfig(
			*portalBaseURL,
			os.Getenv("DISCORD_CLIENT_ID"),
			os.Getenv("DISCORD_CLIENT_SECRET"),
			[]byte(os.Getenv("PORTAL_COOKIE_SECRET")),
			store,
			logger,
		)
		if err != nil {
			return fmt.Errorf("portal: %w", err)
		}
		portalHandler = portal.New(cfg)
	}

	handler := newRouter(cache, logger, *mapsDir, auth, portalHandler)

	srv := &http.Server{
		Addr:         *addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logger.Info("mvd-api starting",
		"addr", *addr, "cacheDir", *cacheDir, "cacheMaxBytes", *cacheMaxBytes,
		"maxParses", cache.MaxParses, "mapsDir", *mapsDir, "authEnabled", auth != nil,
		"schemaVersion", result.CurrentSchemaVersion)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func newLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
