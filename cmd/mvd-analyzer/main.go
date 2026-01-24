package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvd-analyzer/internal/analyzer"
	"github.com/mvd-analyzer/internal/api"
	"github.com/mvd-analyzer/internal/hub"
	"github.com/mvd-analyzer/internal/web"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "analyze":
		analyzeCmd(os.Args[2:])
	case "serve":
		serveCmd(os.Args[2:])
	case "hub":
		hubCmd(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		// Treat first arg as filename for backward compatibility
		analyzeCmd(os.Args[1:])
	}
}

func printUsage() {
	fmt.Println("MVD Demo Analyzer")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  mvd-analyzer analyze <file.mvd> [options]  Analyze an MVD demo")
	fmt.Println("  mvd-analyzer serve [options]               Start web dashboard server")
	fmt.Println("  mvd-analyzer hub <gameId|url> [options]    Download and analyze from QuakeWorld Hub")
	fmt.Println("  mvd-analyzer help                          Show this help")
	fmt.Println()
	fmt.Println("Analyze Options:")
	fmt.Println("  -o, --output <format>   Output format: text, json, tracks (default: text)")
	fmt.Println()
	fmt.Println("Serve Options:")
	fmt.Println("  -p, --port <port>       Port to listen on (default: 8080)")
	fmt.Println()
	fmt.Println("Hub Options:")
	fmt.Println("  -p, --port <port>       Port for web server (default: 8080)")
	fmt.Println("  -d, --dir <directory>   Directory to save demo (default: current)")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  mvd-analyzer analyze demo.mvd")
	fmt.Println("  mvd-analyzer analyze -o json demo.mvd")
	fmt.Println("  mvd-analyzer analyze -o tracks demo.mvd    Export player movement tracks")
	fmt.Println("  mvd-analyzer serve -p 3000")
	fmt.Println("  mvd-analyzer hub 188692")
	fmt.Println("  mvd-analyzer hub \"https://hub.quakeworld.nu/games/?gameId=188692\"")
}

func analyzeCmd(args []string) {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	output := fs.String("o", "text", "Output format: text, json, tracks")
	fs.StringVar(output, "output", "text", "Output format: text, json, tracks")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: no file specified\n")
		os.Exit(1)
	}

	filePath := fs.Arg(0)

	// Create registry with default analyzers
	registry := analyzer.NewDefaultRegistry()

	// Analyze the file
	result, err := registry.Analyze(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Output results
	switch *output {
	case "json":
		outputJSON(result)
	case "tracks":
		outputTracks(result)
	default:
		outputText(result)
	}
}

func outputJSON(result *analyzer.Result) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}

func outputTracks(result *analyzer.Result) {
	tracks := analyzer.ExtractTracks(result)
	if tracks == nil {
		fmt.Fprintln(os.Stderr, "Error: no timeline data available for track extraction")
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(tracks)
}

func outputText(result *analyzer.Result) {
	fmt.Println("=== MVD Demo Analysis ===")
	fmt.Printf("File: %s\n", result.FilePath)
	fmt.Printf("Duration: %.1f seconds\n", result.Duration)
	fmt.Println()

	if result.Match != nil {
		fmt.Println("--- Match Summary ---")
		fmt.Printf("Map: %s\n", result.Match.Map)
		if result.Match.GameDir != "" {
			fmt.Printf("Game: %s\n", result.Match.GameDir)
		}
		fmt.Println()

		if len(result.Match.Teams) > 0 {
			fmt.Println("Teams:")
			for _, t := range result.Match.Teams {
				fmt.Printf("  %s: %d frags\n", t.Name, t.Frags)
			}
			fmt.Println()
		}

		if len(result.Match.Players) > 0 {
			fmt.Println("Players:")
			for _, p := range result.Match.Players {
				teamStr := ""
				if p.Team != "" {
					teamStr = fmt.Sprintf(" [%s]", p.Team)
				}
				fmt.Printf("  %s%s: %d frags\n", p.Name, teamStr, p.Frags)
			}
			fmt.Println()
		}
	}

	if result.Frags != nil {
		fmt.Println("--- Frag Analysis ---")
		fmt.Printf("Total frags detected: %d\n", result.Frags.TotalFrags)

		if len(result.Frags.ByWeapon) > 0 {
			fmt.Println("\nFrags by weapon:")
			for weapon, count := range result.Frags.ByWeapon {
				fmt.Printf("  %s: %d\n", weapon, count)
			}
		}

		if len(result.Frags.ByPlayer) > 0 {
			fmt.Println("\nPlayer stats:")
			for name, stats := range result.Frags.ByPlayer {
				fmt.Printf("  %s: %d kills, %d deaths\n", name, stats.Kills, stats.Deaths)
			}
		}

		// Show recent frags (last 10)
		if len(result.Frags.Frags) > 0 {
			fmt.Println("\nRecent frags:")
			start := len(result.Frags.Frags) - 10
			if start < 0 {
				start = 0
			}
			for _, f := range result.Frags.Frags[start:] {
				timeStr := formatTime(f.Time)
				if f.IsSuicide {
					fmt.Printf("  [%s] %s suicided (%s)\n", timeStr, f.Victim, f.Weapon)
				} else {
					tkStr := ""
					if f.IsTeamKill {
						tkStr = " [TK]"
					}
					fmt.Printf("  [%s] %s killed %s (%s)%s\n", timeStr, f.Killer, f.Victim, f.Weapon, tkStr)
				}
			}
		}
	}

	if len(result.Errors) > 0 {
		fmt.Println("\n--- Errors ---")
		for _, e := range result.Errors {
			fmt.Printf("  %s\n", e)
		}
	}
}

func formatTime(seconds float64) string {
	mins := int(seconds) / 60
	secs := int(seconds) % 60
	return fmt.Sprintf("%d:%02d", mins, secs)
}

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("p", 8080, "Port to listen on")
	fs.IntVar(port, "port", 8080, "Port to listen on")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	addr := fmt.Sprintf(":%d", *port)

	fmt.Printf("Starting web server on port %d...\n", *port)
	fmt.Printf("Web dashboard: http://localhost%s\n", addr)
	fmt.Println("Press Ctrl+C to stop")

	server := api.NewServer(web.StaticFiles)
	if err := server.ListenAndServe(addr); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// Helper to check if string contains any of the substrings
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func hubCmd(args []string) {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	port := fs.Int("p", 8080, "Port for web server")
	fs.IntVar(port, "port", 8080, "Port for web server")
	dir := fs.String("d", ".", "Directory to save demo")
	fs.StringVar(dir, "dir", ".", "Directory to save demo")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: no game ID or URL specified\n")
		fmt.Fprintf(os.Stderr, "Usage: mvd-analyzer hub <gameId|url>\n")
		os.Exit(1)
	}

	input := fs.Arg(0)

	// Parse game ID from input
	gameID, err := hub.ParseGameID(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Fetching game info for ID %d...\n", gameID)

	// Create hub client and fetch game info
	client := hub.NewClient()
	game, err := client.GetGame(gameID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching game: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Game: %s %s on %s\n", game.Mode, game.Matchtag, game.Map)
	if len(game.Teams) >= 2 {
		fmt.Printf("Teams: %s (%d) vs %s (%d)\n",
			game.Teams[0].Name, game.Teams[0].Frags,
			game.Teams[1].Name, game.Teams[1].Frags)
	}

	// Generate filename and download path
	filename := game.GenerateDemoFilename()
	destPath := filepath.Join(*dir, filename)

	fmt.Printf("Downloading demo to %s...\n", destPath)

	// Download demo
	if err := client.DownloadDemo(game, destPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading demo: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Download complete. Analyzing...")

	// Analyze the downloaded demo
	registry := analyzer.NewDefaultRegistry()
	result, err := registry.Analyze(destPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error analyzing demo: %v\n", err)
		os.Exit(1)
	}

	// Start web server with results
	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("\nStarting web server on port %d...\n", *port)
	fmt.Printf("Web dashboard: http://localhost%s\n", addr)
	fmt.Printf("QuakeWorld Hub: https://hub.quakeworld.nu/games/?gameId=%d\n", gameID)
	fmt.Println("Press Ctrl+C to stop")

	server := api.NewServer(web.StaticFiles)
	server.SetInitialResult(result)
	if err := server.ListenAndServe(addr); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
