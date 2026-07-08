// mvd-api hosts the qwanalytics view surface over HTTP REST, backed by
// an on-disk two-tier cache that fetches demos from hub.quakeworld.nu
// on demand.
//
// Usage:
//
//	mvd-api [flags]
//	mvd-api version
//	mvd-api cache stats [-cache-dir DIR]
//	mvd-api cache prune [-cache-dir DIR] [-max-bytes N | -older-than 30d | -all]
//	mvd-api keys issue  -auth-dir DIR [-service] [-note S] [-discord-id ID] [-discord-name N]
//	mvd-api keys revoke -auth-dir DIR (-key K | -hash H | -discord-id ID)
//	mvd-api keys list   -auth-dir DIR
//
// Flags:
//
//	-addr             listen address (default ":8080")
//	-cache-dir        on-disk cache root (default $XDG_CACHE_HOME/qw-mvd or ~/.cache/qw-mvd)
//	-cache-max-bytes  cache disk budget in bytes; background GC evicts when over (0 disables)
//	-max-parses       max concurrent download+parse operations (0 = max(1, NumCPU/2))
//	-log-format       text | json (default "text")
//	-auth-dir         keys.json dir; when set, /v1/* requires an API key (empty = no auth)
//	-rate-user        per-key req/s for portal keys (default 5); -burst-user (default 20)
//	-rate-service     per-key req/s for service keys (default 50); -burst-service (default 200)
//
// See mvd-api/README.md for the endpoint surface.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const usage = "usage: mvd-api [flags] | version | cache <stats|prune> | keys <issue|revoke|list>"

// dispatch resolves args (os.Args[1:]) to a subcommand. The default is
// "serve" — when args are empty or the first arg is a flag (starts with "-").
// A first arg that is a positional (does not start with "-") must be a known
// subcommand; anything else returns ok=false so a typo'd subcommand
// ("mvd-api serv") is rejected loudly instead of silently booting a server.
func dispatch(args []string) (cmd string, ok bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "serve", true
	}
	switch args[0] {
	case "serve", "version", "cache", "keys":
		return args[0], true
	default:
		return args[0], false
	}
}

func main() {
	args := os.Args[1:]
	cmd, ok := dispatch(args)
	if !ok {
		fmt.Fprintf(os.Stderr, "mvd-api: unknown subcommand %q\n%s\n", args[0], usage)
		os.Exit(1)
	}

	switch cmd {
	case "version":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"hash":      GitHash,
			"tag":       GitTag,
			"buildDate": BuildDate,
		})
		return
	case "cache":
		if err := runCache(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "mvd-api: %v\n", err)
			os.Exit(1)
		}
		return
	case "keys":
		if err := runKeys(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "mvd-api: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// serve. An explicit "serve" positional consumes the arg; flags-as-args
	// pass through unchanged.
	serveArgs := args
	if len(args) > 0 && args[0] == "serve" {
		serveArgs = args[1:]
	}
	if err := runServe(serveArgs); err != nil {
		fmt.Fprintf(os.Stderr, "mvd-api: %v\n", err)
		os.Exit(1)
	}
}
