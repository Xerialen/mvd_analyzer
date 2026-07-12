# Dev container

Self-contained dev environment for mvd-analyzer — Zed
(["reopen in container"](https://zed.dev/docs/dev-containers)), VS Code, or
the `devcontainer` CLI. Builds from [`Dockerfile`](Dockerfile); no prebuilt
image or parent Makefile needed.

## In the image

Pinned Go (matches `go.work`) + `gopls`/`dlv`/`goimports`, `gh`, `jq`, `git`,
`tmux`, `emacs` (go-mode), Claude Code, build deps. No Node (web is Go→WASM).

## Identity & secrets (host-injected)

Tools only in the image. Export on the host; pulled in via `remoteEnv`
(`${localEnv:VAR}` resolves in the env that launched Zed):

```bash
export GIT_AUTHOR_NAME="Your Name"
export GIT_AUTHOR_EMAIL="you@example.com"
export GH_TOKEN="…"          # optional; or `gh auth login` in the container
```

Unset vars resolve to empty and are ignored. Use `remoteEnv`, not
`containerEnv` (the latter breaks the build on values with spaces). If the
name ever still fails, drop `GIT_*_NAME` and mount your gitconfig:
`"mounts": ["source=${localEnv:HOME}/.gitconfig,target=/home/dev/.gitconfig,type=bind,readonly"]`.

Preferences are committed as generic defaults: [`tmux.conf`](tmux.conf),
[`bashrc.extra`](bashrc.extra), [`emacs-init.el`](emacs-init.el).

## UID / GID

User `dev` at UID/GID **1000** (the `Dockerfile` default; no personal values).
macOS/Windows Docker Desktop remaps automatically. On Linux with a non-1000
host UID, override: `"build": { "args": { "UID": "1001", "GID": "1001" } }`.
