# MCP client integration

`mvd-mcp` is a stdio MCP server that forwards every tool call over
HTTP to a running `mvd-api`. Wire it into Claude Desktop, Claude Code,
Cursor, or any other MCP client.

## Where to get the binary

Cross-compiled binaries for Linux, macOS (Intel + Apple Silicon), and
Windows are produced by:

```bash
make build-mcp-windows        # dist/mvd-mcp-windows-amd64.exe
make build-mcp-darwin         # dist/mvd-mcp-darwin-{amd64,arm64}
make build-mcp-linux          # dist/mvd-mcp-linux-amd64
make build-all-platforms      # all of the above + mvd-api binaries
```

Once a release pipeline exists, these will attach to GitHub Releases.

**Windows:** unsigned `.exe` triggers a SmartScreen warning. Right-click
→ Properties → Unblock, or click *More info → Run anyway*.

**macOS:** Gatekeeper blocks unsigned binaries. Either right-click →
Open the first time, or run:

```bash
xattr -d com.apple.quarantine mvd-mcp-darwin-arm64
```

## Claude Desktop

Edit `claude_desktop_config.json`:

| OS | Path |
|---|---|
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Linux | `~/.config/Claude/claude_desktop_config.json` |

Add an `mcpServers.mvd-mcp` entry, then restart Claude Desktop.

**Hosted mvd-api (recommended):**

```json
{
  "mcpServers": {
    "mvd-mcp": {
      "command": "C:\\Tools\\mvd-mcp.exe",
      "args": [
        "-api", "https://mvd-api.example.com",
        "-label", "mcp-claude-desktop"
      ]
    }
  }
}
```

**Local mvd-api on `localhost:8080`:**

Start the API in a separate terminal:

```bash
mvd-api -addr :8080
```

Then point the shim at it:

```json
{
  "mcpServers": {
    "mvd-mcp": {
      "command": "/usr/local/bin/mvd-mcp",
      "args": [
        "-api", "http://localhost:8080",
        "-label", "mcp-claude-desktop-local"
      ]
    }
  }
}
```

## Claude Code

Two options:

**Project-scoped via `.mcp.json`** in the repo root (recommended —
ships with the project):

```json
{
  "mcpServers": {
    "mvd-mcp": {
      "command": "/workspace/mvd-analyzer/dist/mvd-mcp",
      "args": ["-api", "http://localhost:8080", "-label", "claude-code"]
    }
  }
}
```

**User-scoped via CLI:**

```bash
claude mcp add mvd-mcp /usr/local/bin/mvd-mcp -api http://localhost:8080 -label claude-code
```

After either, restart Claude Code. Run `/mcp` in the prompt to verify
the tools appear (the full list is the tool table in
[README.md](README.md)).

To auto-approve tool calls (skip the permission prompt each time), add
to `.claude/settings.local.json`:

```json
{
  "permissions": {
    "allow": ["mcp__mvd-mcp__*"]
  }
}
```

## Cursor / other MCP clients

The same `.mcp.json` shape works; consult your client's docs for the
config file path.

## Smoke test

After your client restarts, the tool list should match the tool table in
[README.md](README.md) — `searchGames` and `loadDemo`, then the per-demo
views (`getOverview`, `getFrags`, `getDamage`, `getAim`, `getBuckets`,
`getEvents`, `getStateAt`, …).

Try a prompt like:

> Load hub game 12345 and tell me the top three frag streaks.

The model should call `loadDemo` first, then `getOverview` (which
surfaces `topStreaks`).
