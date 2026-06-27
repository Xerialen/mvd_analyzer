FROM ubuntu:24.04

# Toolchain image for mvd-analyzer. This holds tools only — no personal
# identity or secrets. Git author/email and GH_TOKEN are injected from the
# host at container-create time via devcontainer.json remoteEnv
# (${localEnv:...}), so nothing personal is baked into the image or the repo.

ARG GO_VERSION=1.25.10
ARG USERNAME=dev
ARG UID=1000
ARG GID=1000
ENV DEBIAN_FRONTEND=noninteractive

# Base packages.
#   gh, jq  — load-bearing: gh drives the push workflow (gh auth
#             git-credential over HTTPS), jq is used in the analyze recipes.
#   ncurses-term, locales — correct 256-colour/truecolour + UTF-8 in the
#             integrated terminal.
# nodejs is intentionally omitted: the web layer is Go->WASM (no package.json),
# so nothing in the repo needs it.
RUN apt-get update && apt-get install -y \
        ca-certificates curl wget gnupg sudo \
        git tmux emacs-nox htop jq \
        build-essential python3 python3-pip python3-venv \
        ncurses-term locales \
    && rm -rf /var/lib/apt/lists/* \
    && locale-gen en_US.UTF-8
ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# GitHub CLI from the official apt repo (newer than Ubuntu universe).
RUN mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
         -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
         > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# Go, pinned. Matches the go.work toolchain (>= 1.25.0), so no toolchain is
# downloaded at runtime. GOTOOLCHAIN stays at the default (auto), so a later
# go.work bump still resolves correctly.
RUN curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz \
        | tar -C /usr/local -xz

# Replace the default ubuntu user (UID 1000) with one matching the host UID/GID
# so bind-mounted files stay owned by you. Free UID/GID 1000 first — the base
# image's `ubuntu` user/group occupies it, and a leftover group there would
# otherwise make groupadd fail.
RUN userdel -r ubuntu 2>/dev/null || true; \
    if getent group ${GID} >/dev/null; then \
        groupdel "$(getent group ${GID} | cut -d: -f1)" 2>/dev/null || true; \
    fi; \
    groupadd -g ${GID} ${USERNAME} && \
    useradd -u ${UID} -g ${GID} -m -s /bin/bash ${USERNAME} && \
    echo "${USERNAME} ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/${USERNAME} && \
    chmod 0440 /etc/sudoers.d/${USERNAME}

USER ${USERNAME}
ENV HOME=/home/${USERNAME}
ENV PATH="/usr/local/go/bin:${HOME}/go/bin:${HOME}/.local/bin:${PATH}"

# Go language tooling, built with the pinned toolchain.
RUN go install golang.org/x/tools/gopls@latest \
    && go install golang.org/x/tools/cmd/goimports@latest \
    && go install github.com/go-delve/delve/cmd/dlv@latest

# Claude Code.
RUN curl -fsSL https://claude.ai/install.sh | bash

# Emacs config (go-mode from MELPA). Best-effort pre-install so the first
# launch is offline-friendly; ignore failure (no network at build time).
COPY --chown=${USERNAME}:${USERNAME} emacs-init.el ${HOME}/.emacs.d/init.el
RUN emacs --batch -l ${HOME}/.emacs.d/init.el \
        --eval "(package-refresh-contents)" \
        --eval "(package-install 'go-mode)" 2>/dev/null || true

# Shell + tmux preferences (no identity/secrets).
COPY --chown=${USERNAME}:${USERNAME} tmux.conf ${HOME}/.tmux.conf
COPY --chown=${USERNAME}:${USERNAME} bashrc.extra ${HOME}/.bashrc.extra
RUN echo '. ~/.bashrc.extra' >> ${HOME}/.bashrc

WORKDIR /workspace
CMD ["/bin/bash", "-l"]
