# Deploy runbook — hosting mvd-api + mvd-mcp

Templates for putting the QuakeWorld MVD analytics service on the public
internet: `mvd-api` (REST + Discord key portal) and `mvd-mcp` (MCP over
streamable HTTP), behind Caddy for TLS. This is the concrete shape of
[PLAN-hosting.md](../PLAN-hosting.md) decision **D9**.

These files are **templates**, not run by CI. Read every `EDIT:` comment
before enabling a unit; the ports, paths, and flag names below must stay
consistent with each other (and with the binaries).

## Surfaces and port map

| Service | Bind | Behind Caddy at |
|---|---|---|
| mvd-api | `localhost:8080` | everything except `/mcp*` |
| mvd-mcp | `localhost:8081` | `/mcp*` (streamable HTTP) |

mvd-mcp serves the MCP handler at exactly `/mcp` (and `/mcp/`); Caddy
passes the path through unchanged, so there is no prefix strip to get
wrong. Both services expose an unauthenticated `GET /healthz`.

**Auth split:** the REST API requires an API key on every `/v1/*` call;
MCP requires none — mvd-mcp holds its own operator-issued **service key**
(`MVD_API_KEY`) and forwards it upstream, so anonymous MCP traffic is
globally throttled by that one key's service rate class. A client that
does present a `qwmvd_…` bearer on MCP gets that key forwarded instead.

## Operator prerequisites (you do these, not the tooling)

See PLAN-hosting.md → **"Operator prerequisites"**. In short:

1. **Discord application.** Developer Portal → New Application → OAuth2.
   Note the **client id** and **client secret**. Register the redirect
   URI `https://<domain>/portal/callback` (add
   `http://localhost:8080/portal/callback` too if you want the local dev
   flow). The portal uses only the `identify` scope.
2. **Domain + VPS.** Pick the domain, provision the host, point DNS
   `A`/`AAAA` records at it before starting Caddy (Caddy provisions the
   TLS cert on first request).

## 1. Build

On a build host with the Go toolchain:

```sh
make build-bin      # produces dist/mvd-api and dist/mvd-mcp
```

(`make build` builds only the WASM web frontend, not the server binaries.)
To cross-compile Linux binaries on another OS, use
`make build-api-linux build-mcp-linux` (or `make build-all-platforms`),
which emit `dist/mvd-api-linux-amd64` and `dist/mvd-mcp-linux-amd64`.

## 2. Place binaries and create the account

```sh
sudo useradd --system --home /opt/mvd --shell /usr/sbin/nologin mvd
sudo mkdir -p /opt/mvd/bin /opt/mvd/cache /opt/mvd/auth
sudo cp dist/mvd-api dist/mvd-mcp /opt/mvd/bin/
sudo chown -R mvd:mvd /opt/mvd
sudo chmod 700 /opt/mvd/auth        # key store — keys.json lives here
```

## 3. Write the secrets file

`/etc/mvd/secrets.env`, root-owned `0600`, referenced by
`mvd-api.service`'s `EnvironmentFile=`. These are the values that must
NEVER appear in flags (they would show in `ps`) or in the repo:

```sh
sudo install -d -m 0755 /etc/mvd
sudo tee /etc/mvd/secrets.env >/dev/null <<'EOF'
DISCORD_CLIENT_ID=your-discord-client-id
DISCORD_CLIENT_SECRET=your-discord-client-secret
# 32+ random bytes; e.g. `openssl rand -base64 48`
PORTAL_COOKIE_SECRET=replace-with-a-long-random-string
EOF
sudo chmod 600 /etc/mvd/secrets.env
```

## 4. Issue the mvd-mcp service key

mvd-mcp authenticates to mvd-api with one operator-issued **service**
key (higher rate class). Issue it with the CLI (not the portal) and put
it in `/etc/mvd/mcp.env`, which `mvd-mcp.service` reads:

```sh
sudo -u mvd /opt/mvd/bin/mvd-api keys issue \
    -auth-dir /opt/mvd/auth -service -note "mvd-mcp"
sudo tee /etc/mvd/mcp.env >/dev/null <<'EOF'
MVD_API_KEY=qwmvd_…
EOF
sudo chmod 600 /etc/mvd/mcp.env
```

The full key is printed **once** — capture it. The same recipe (with
`-note "mvd-web"`) issues the web client's key in phase 17+. End users
who want direct REST access get their own keys from the portal at
`https://<domain>/portal`.

## 5. Install the units and Caddy config

```sh
# systemd units — edit User/paths/flags/-portal-base-url first.
sudo cp deploy/mvd-api.service deploy/mvd-mcp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mvd-api mvd-mcp

# Caddy — set the domain via the environment.
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl set-environment MVD_DOMAIN=qw.example.com   # or edit the file
sudo systemctl restart caddy
```

## 6. Smoke-test checklist

Run these from a client machine (replace `qw.example.com` and
`qwmvd_…`). Each line notes the expected result.

```sh
# a. Liveness.
#    mvd-api's healthz is the public probe (through Caddy, TLS):
curl -sS https://qw.example.com/healthz            # {"ok":true,...} (mvd-api)
#    mvd-mcp's healthz is on its own port; probe it ON THE BOX (Caddy only
#    forwards /mcp* to the streamable handler, not /healthz):
curl -sS http://localhost:8081/healthz             # {"ok":true} (mvd-mcp)

# b. Portal flow (browser): visit https://qw.example.com/portal,
#    "Sign in with Discord", authorize, land on /portal/key, generate a
#    key. Copy the qwmvd_… value (shown once).

# c. Keyed REST call succeeds; the same call keyless is 401.
curl -sS -H "Authorization: Bearer qwmvd_…" \
     https://qw.example.com/v1/auth/check -o /dev/null -w '%{http_code}\n'   # 204
curl -sS https://qw.example.com/v1/auth/check -o /dev/null -w '%{http_code}\n' # 401

# d. MCP initialize needs NO key (the shim forwards its service key).
claude mcp add --transport http mvd https://qw.example.com/mcp
#    then in Claude: the mvd tools list; call getOverview on a loaded demo.
#    If tool calls error with "unauthorized", MVD_API_KEY in
#    /etc/mvd/mcp.env is missing/revoked — check journalctl -u mvd-mcp.

# e. Rate limit: hammer past the burst (default 20 for a user key) and
#    observe 429 + Retry-After.
for i in $(seq 1 40); do \
  curl -sS -H "Authorization: Bearer qwmvd_…" \
       https://qw.example.com/v1/auth/check -o /dev/null -w '%{http_code} '; \
done; echo    # a run of 204 then 429 once the bucket drains
```

## Notes

- **Cache growth** is bounded by `-cache-max-bytes`, but the eviction GC
  runs **only on cache writes** — so the on-disk byte budget can overshoot
  briefly and self-heals on the next write (GC evicts oldest by mtime).
  Size `-cache-max-bytes` to the disk with a little headroom.
- **Warm-cache reads are not bounded by `-max-parses`.** `-max-parses`
  caps concurrent cold parses only; a burst of requests across many
  distinct *already-cached* demos each triggers a tier-2 gob decode into
  memory with no concurrency cap. Size the box's RAM for that fan-out, not
  just for `-max-parses` cold parses.
- **A revoked key dies on the next request** — keys are validated by
  mvd-api per call, never cached by the shim. Revoking the mvd-mcp
  service key turns off all anonymous MCP tool calls at once.
- **Anonymous MCP shares one rate bucket.** All keyless MCP traffic is
  throttled together under the single mvd-mcp service key's service class,
  so one abusive anonymous caller can 429 anonymous MCP for everyone.
  `-rate-service` / `-burst-service` (on mvd-api) is the dial; `keys
  revoke` on that service key is the kill switch (see the revoked-key note
  above). Callers who present their own `qwmvd_…` key get their own bucket.
- **Logs**: only mvd-api emits JSON (`-log-format json`) to journald, so
  `journalctl -u mvd-api -o cat | jq` works on those lines. mvd-mcp logs
  plain text (slog TextHandler, no flag) — read it with `journalctl -u
  mvd-mcp`, don't pipe it through jq. mvd-api's access-log identity is the
  key's note / Discord name / hash prefix — never the key.
