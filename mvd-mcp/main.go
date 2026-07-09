// mvd-mcp is an MCP server that forwards every tool call to a running
// mvd-api. The shim is intentionally minimal: it does not import any
// qwanalytics code, so the distributable binary is small (~5 MB) and
// stable against analytics-side changes.
//
// It has two transports:
//
//   - stdio (default): one process per client, launched by the client.
//   - streamable HTTP (-http ADDR): a long-lived server for hosted use.
//     MCP itself is unauthenticated; the shim authenticates to mvd-api
//     with its own service key (MVD_API_KEY).
//
// Usage:
//
//	mvd-mcp -api URL [-label TAG] [-timeout SECONDS]           # stdio
//	mvd-mcp -http ADDR -api URL [-timeout SECONDS]             # HTTP
//	mvd-mcp version
//
// Flags:
//
//	-api      required: base URL of a running mvd-api (e.g. https://qw-mvd.example.com)
//	-http     serve MCP over streamable HTTP on ADDR (e.g. :8081) instead of stdio
//	-label    stdio only: non-secret request-source tag, forwarded as Authorization: Bearer <label>
//	-timeout  per-request HTTP timeout in seconds (default 60)
//
// Environment:
//
//	MVD_API_KEY  API key forwarded as Authorization: Bearer on every proxied
//	             mvd-api call. Required (as an operator-issued service key)
//	             when the target mvd-api runs with -auth-dir; an env var, not
//	             a flag, so the secret never shows in `ps`. When set in stdio
//	             mode it supersedes -label.
//
// For local MCP, run mvd-api on localhost and point -api at it:
//
//	mvd-api -addr :8080 &
//	mvd-mcp -api http://localhost:8080
//
// In -http mode incoming MCP requests need no Authorization header. A
// request that does carry a `Bearer qwmvd_…` key has that key forwarded
// upstream instead of the service key (per-key attribution).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverName = "mvd-mcp"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"hash":      GitHash,
			"tag":       GitTag,
			"buildDate": BuildDate,
		})
		return
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "mvd-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mvd-mcp", flag.ContinueOnError)
	apiURL := fs.String("api", "", "required: mvd-api base URL (e.g. http://localhost:8080)")
	httpAddr := fs.String("http", "", "serve MCP over streamable HTTP on this address (e.g. :8081) instead of stdio")
	label := fs.String("label", "", "stdio only: non-secret request-source label, forwarded as Authorization: Bearer <label>")
	timeoutS := fs.Int("timeout", 60, "per-request HTTP timeout in seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apiURL == "" {
		fs.Usage()
		return errors.New("-api URL is required")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	timeout := time.Duration(*timeoutS) * time.Second

	// The upstream credential lives in the environment, never a flag (flags
	// show in `ps`; same rule as mvd-api's portal secrets).
	apiKey := os.Getenv("MVD_API_KEY")

	// HTTP mode is a distinct transport; -label is meaningless there (the
	// upstream Authorization is the service key, or a caller-supplied qwmvd_
	// bearer). Warn rather than silently ignore it.
	if *httpAddr != "" {
		if *label != "" {
			logger.Warn("-label is ignored in -http mode; MVD_API_KEY / per-request keys are used instead")
		}
		return runHTTP(*httpAddr, *apiURL, apiKey, timeout, logger)
	}

	// stdio: a real key beats the non-secret label — the label only exists for
	// access-log attribution against a no-auth (localhost) mvd-api, while the
	// key is what an auth-enabled mvd-api requires.
	bearer := *label
	if apiKey != "" {
		if *label != "" {
			logger.Warn("both MVD_API_KEY and -label set; using the key")
		}
		bearer = apiKey
	}

	logger.Info("mvd-mcp starting", "api", *apiURL, "label", *label, "keySet", apiKey != "")

	backend := newProxyBackend(*apiURL, bearer, timeout)
	search := newSupabaseClient(timeout)
	srv := newMCPServer(backend, search)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		sig := <-sigCh
		logger.Info("mvd-mcp shutting down", "signal", sig.String())
		cancel()
	}()

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		// stdin EOF / ctx cancel = clean shutdown the way MCP clients
		// signal "I'm done." The SDK wraps EOF in its own
		// "server is closing" error string; match on that too.
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
			strings.Contains(err.Error(), "server is closing") {
			return nil
		}
		return fmt.Errorf("mcp run: %w", err)
	}
	return nil
}

// newMCPServer builds an mcp.Server with every tool registered against the
// given proxy backend and hub searcher. Shared by stdio mode (one server for
// the process lifetime) and HTTP mode (one per request, with a per-request
// backend carrying the caller's key).
func newMCPServer(backend MCPBackend, search searcher) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Title:   "QuakeWorld MVD analytics (proxy to mvd-api + hub Supabase search)",
		Version: GitTag,
	}, nil)
	registerTools(srv, backend, search)
	return srv
}
