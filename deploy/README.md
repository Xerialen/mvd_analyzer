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
make build          # produces dist/mvd-api and dist/mvd-mcp
```

(or `make build-linux` for cross-compiled `dist/mvd-*-linux-amd64`.)

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

mvd-mcp needs no secrets file — every MCP request carries its own API
key, which mvd-mcp validates against mvd-api and forwards on each proxied
call (D7).

## 4. Issue a service key for the web client (optional, phase 17+)

The first-party web app gets one operator-issued **service** key (higher
rate class). Issue it with the CLI (not the portal):

```sh
sudo -u mvd /opt/mvd/bin/mvd-api keys issue \
    -auth-dir /opt/mvd/auth -service -note "mvd-web"
```

The full key is printed **once** — capture it. End users get their own
keys from the portal at `https://<domain>/portal`.

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

# d. MCP initialize with a key succeeds; without a key is 401 at init.
claude mcp add --transport http mvd https://qw.example.com/mcp \
     --header "Authorization: Bearer qwmvd_…"
#    then in Claude: the mvd tools list; call getOverview on a loaded demo.

# e. Rate limit: hammer past the burst (default 20 for a user key) and
#    observe 429 + Retry-After.
for i in $(seq 1 40); do \
  curl -sS -H "Authorization: Bearer qwmvd_…" \
       https://qw.example.com/v1/auth/check -o /dev/null -w '%{http_code} '; \
done; echo    # a run of 204 then 429 once the bucket drains
```

## Notes

- **Cache growth** is bounded by `-cache-max-bytes` (background GC evicts
  oldest by mtime). Size it to the disk.
- **A revoked key dies on the next request** — MCP sessions are not
  pinned to a validated key beyond the current call (D7).
- **Logs**: both services log JSON to journald (`journalctl -u mvd-api`).
  The access log's identity is the key's note / Discord name / hash
  prefix — never the key.
