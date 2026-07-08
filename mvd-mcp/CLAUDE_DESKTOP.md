# MCP client integration

`mvd-mcp` forwards every tool call over HTTP to a running `mvd-api`.
There are two ways to connect a client:

- **Local stdio binary** — you run `mvd-mcp` (a small binary) and the
  client launches it over stdio. This is the bulk of this guide.
- **Hosted HTTP URL** — you point the client at a hosted
  `https://<domain>/mcp` endpoint with an API key, and run **no** local
  binary. See [Hosted HTTP mode](#hosted-http-mode-no-local-binary).

Wire either into Claude Desktop, Claude Code, Cursor, or any other MCP
client.

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

## Hosted HTTP mode (no local binary)

When the operator runs `mvd-mcp -http` behind a public domain (see
[`../deploy/README.md`](../deploy/README.md)), a client can connect
straight to the URL — no local `mvd-mcp` binary to install. You need an
API key (`qwmvd_…`) from the portal at `https://<domain>/portal`.

**Claude Code (CLI):**

```bash
claude mcp add --transport http mvd https://<domain>/mcp \
    --header "Authorization: Bearer qwmvd_…"
```

**`.mcp.json` / other HTTP-capable clients:**

```json
{
  "mcpServers": {
    "mvd": {
      "url": "https://<domain>/mcp",
      "headers": { "Authorization": "Bearer qwmvd_…" }
    }
  }
}
```

The key is sent on every request; the hosted server validates it and
rejects a missing/invalid key with `401`. See the
[Hosted / HTTP mode](README.md#hosted--http-mode) section of the README
for the auth model.

> Claude Desktop's stable config schema is stdio-oriented; if your
> Desktop build does not yet support a remote HTTP MCP server, use the
> local stdio binary above pointed at the hosted `mvd-api` instead.

## Smoke test

After your client restarts, the tool list should match the tool table in
[README.md](README.md) — `searchGames` and `loadDemo`, then the per-demo
views (`getOverview`, `getFrags`, `getDamage`, `getAim`, `getBuckets`,
`getEvents`, `getStateAt`, …).

Try a prompt like:

> Load hub game 12345 and tell me the top three frag streaks.

The model should call `loadDemo` first, then `getOverview` (which
surfaces `topStreaks`).
