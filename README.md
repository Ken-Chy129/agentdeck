# AgentDeck

Personal control plane for my coding-agent setup across machines — skills today, MCP servers,
CLI configs and credentials next: a web console on my VPS
plus a tiny `agentdeck` CLI on every machine. Machines pull; the server never needs to reach them.

```
browser ──▶ agentdeckd (Go, SQLite, embedded web UI)  ◀── HTTPS ── agentdeck sync (each machine)
              skills · versions · machines · assignments · jobs · sync logs
```

## What it does

- **Skill library**: versioned, content-addressed skill bundles. Create/edit in the browser,
  upload a tar.gz, or `agentdeck push ~/.agents/skills/foo` from any machine.
- **Distribution**: assign skills to machines (per-machine page or the matrix view).
  `agentdeck sync` installs/updates/removes so `~/.agents/skills/` matches the server, then
  symlinks into `~/.claude/skills/`. Server wins; local edits are backed up to
  `~/.agents/skills/.agentdeck-backup/` first. Skills that are not assigned are never touched.
- **CLI inventory**: each sync reports node/npm/brew/go/… versions and agent CLIs
  (claude, codex, gemini, …) with their install source and path. The console compares against
  npm and shows what is behind, with a copyable upgrade command.
- **Jobs**: queue `npm_upgrade` / `brew_upgrade` for a machine; it runs on the next sync and
  posts the output back. Job types are a whitelist and arguments are validated on the client.

## Server

```sh
docker compose -f deploy/docker-compose.yml up -d --build   # listens on 127.0.0.1:8480
cat /opt/agentdeck/data/admin_token                          # paste into the web login
```

Put it behind Caddy/nginx with TLS (see `deploy/Caddyfile.snippet`). Env: `AGENTDECK_ADDR`,
`AGENTDECK_DATA`, `AGENTDECK_ADMIN_TOKEN` (optional; otherwise generated into `data/admin_token`).

## Machine

```sh
go install github.com/Ken-Chy129/agentdeck/cmd/agentdeck@latest   # or grab a binary from dist/
agentdeck login https://deck.example.com <enroll-token> --name mbp
agentdeck sync                     # once
agentdeck install-schedule --watch # stay online: console commands run in seconds
agentdeck install-schedule         # or timer-only: sync every 15 min
agentdeck push ~/.agents/skills/my-skill --note "tweak"
agentdeck status
agentdeck inventory
```

`--watch` keeps the CLI resident and long-polls the server, so a command typed in
the console's 终端 tab (or an upgrade button) runs within a couple of seconds. The
machine always dials out, so this works from boxes the server can't reach. On Linux
run `loginctl enable-linger $USER` so the unit survives logout.

Config: `~/.config/agentdeck/config.json` (0600). Lockfile: `~/.agents/skills/.agentdeck-lock.json`.

## Layout

```
cmd/agentdeckd        server entrypoint
cmd/agentdeck         machine CLI
internal/api         HTTP handlers (/api/admin/* admin token, /api/agent/* machine token)
internal/store       SQLite schema + queries
internal/bundle      tar.gz pack/unpack, stable digest, safe dir swap
internal/inventory   version collection (names/versions/paths only — never env or config contents)
internal/sync        client: config, lockfile, reconcile loop, symlinks, job whitelist
internal/protocol    JSON shapes shared by both sides
web/static           single-page console (vanilla JS, embedded into the binary)
deploy/              Dockerfile compose + Caddy snippet
```
