package main

import (
	"flag"
	"fmt"

	"github.com/mvd-analyzer/mvd-api/internal/authkeys"
)

// runKeys dispatches the `mvd-api keys <issue|revoke|list>` subcommands, which
// operate directly on the keys.json under -auth-dir (no server, no network).
func runKeys(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mvd-api keys <issue|revoke|list> -auth-dir DIR [flags]")
	}
	switch args[0] {
	case "issue":
		return runKeysIssue(args[1:])
	case "revoke":
		return runKeysRevoke(args[1:])
	case "list":
		return runKeysList(args[1:])
	default:
		return fmt.Errorf("unknown keys subcommand %q (want issue|revoke|list)", args[0])
	}
}

func openAuthStore(dir string) (*authkeys.Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("-auth-dir is required")
	}
	return authkeys.Open(dir)
}

func runKeysIssue(args []string) error {
	fs := flag.NewFlagSet("keys issue", flag.ContinueOnError)
	authDir := fs.String("auth-dir", "", "directory holding keys.json")
	service := fs.Bool("service", false, "issue a service key (looser rate class; for mvd-web/ops, not the portal)")
	note := fs.String("note", "", "free-text note stored with the key (e.g. \"mvd-web\")")
	discordID := fs.String("discord-id", "", "Discord user id to bind the key to (issuing revokes that user's prior key)")
	discordName := fs.String("discord-name", "", "Discord display name (metadata only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := openAuthStore(*authDir)
	if err != nil {
		return err
	}
	key, rec, err := store.Issue(*discordID, *discordName, *service, *note)
	if err != nil {
		return err
	}
	// The full key is printed exactly once, here. Nothing else ever prints it.
	fmt.Printf("key: %s\n", key)
	fmt.Printf("  (store this now — it is not recoverable)\n")
	fmt.Printf("hashPrefix:  %s\n", rec.HashPrefix())
	fmt.Printf("service:     %t\n", rec.Service)
	if rec.Note != "" {
		fmt.Printf("note:        %s\n", rec.Note)
	}
	if rec.DiscordID != "" {
		fmt.Printf("discordId:   %s\n", rec.DiscordID)
	}
	if rec.DiscordName != "" {
		fmt.Printf("discordName: %s\n", rec.DiscordName)
	}
	fmt.Printf("created:     %s\n", rec.Created)
	return nil
}

func runKeysRevoke(args []string) error {
	fs := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
	authDir := fs.String("auth-dir", "", "directory holding keys.json")
	key := fs.String("key", "", "revoke by full key")
	hash := fs.String("hash", "", "revoke by key hash (full or as shown by `keys list`... full hash required)")
	discordID := fs.String("discord-id", "", "revoke every active key bound to this Discord id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := openAuthStore(*authDir)
	if err != nil {
		return err
	}
	n, err := store.Revoke(*key, *hash, *discordID)
	if err != nil {
		return err
	}
	fmt.Printf("revoked %d key(s)\n", n)
	return nil
}

func runKeysList(args []string) error {
	fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
	authDir := fs.String("auth-dir", "", "directory holding keys.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := openAuthStore(*authDir)
	if err != nil {
		return err
	}
	recs := store.List()
	if len(recs) == 0 {
		fmt.Println("no keys")
		return nil
	}
	// Prefix + metadata only — never the full key or full hash.
	fmt.Printf("%-8s  %-7s  %-19s  %-19s  %-12s  %s\n",
		"PREFIX", "STATUS", "CREATED", "REVOKED", "DISCORD", "NOTE")
	for _, r := range recs {
		status := "active"
		if !r.Active() {
			status = "revoked"
		}
		class := "user"
		if r.Service {
			class = "service"
		}
		discord := r.DiscordName
		if discord == "" {
			discord = r.DiscordID
		}
		note := r.Note
		if note == "" {
			note = "(" + class + ")"
		}
		fmt.Printf("%-8s  %-7s  %-19s  %-19s  %-12s  %s\n",
			r.HashPrefix(), status, r.Created, r.Revoked, discord, note)
	}
	return nil
}
