# SkillHub

Personal control plane for my coding-agent setup across machines: a web console on my VPS
plus a tiny `skillhub` CLI on every machine. Machines pull; the server never needs to reach them.

```
browser ──▶ skillhubd (Go, SQLite, embedded web UI)  ◀── HTTPS ── skillhub sync (each machine)
              skills · versions · machines · assignments · jobs · sync logs
```

## What it does

- **Skill library**: versioned, content-addressed skill bundles. Create/edit in the browser,
  upload a tar.gz, or `skillhub push ~/.agents/skills/foo` from any machine.
- **Distribution**: assign skills to machines (per-machine page or the matrix view).
  `skillhub sync` installs/updates/removes so `~/.agents/skills/` matches the server, then
  symlinks into `~/.claude/skills/`. Server wins; local edits are backed up to
  `~/.agents/skills/.skillhub-backup/` first. Skills that are not assigned are never touched.
- **CLI inventory**: each sync reports node/npm/brew/go/… versions and agent CLIs
  (claude, codex, gemini, …) with their install source and path. The console compares against
  npm and shows what is behind, with a copyable upgrade command.
- **Jobs**: queue `npm_upgrade` / `brew_upgrade` for a machine; it runs on the next sync and
  posts the output back. Job types are a whitelist and arguments are validated on the client.

## Server

```sh
docker compose -f deploy/docker-compose.yml up -d --build   # listens on 127.0.0.1:8480
cat /opt/skillhub/data/admin_token                          # paste into the web login
```

Put it behind Caddy/nginx with TLS (see `deploy/Caddyfile.snippet`). Env: `SKILLHUB_ADDR`,
`SKILLHUB_DATA`, `SKILLHUB_ADMIN_TOKEN` (optional; otherwise generated into `data/admin_token`).

## Machine

```sh
go install github.com/Ken-Chy129/skillhub/cmd/skillhub@latest   # or grab a binary from dist/
skillhub login https://skillhub.example.com <enroll-token> --name mbp
skillhub sync                     # once
skillhub install-schedule         # every 15 min via launchd / systemd --user
skillhub push ~/.agents/skills/my-skill --note "tweak"
skillhub status
skillhub inventory
```

Config: `~/.config/skillhub/config.json` (0600). Lockfile: `~/.agents/skills/.skillhub-lock.json`.

## Layout

```
cmd/skillhubd        server entrypoint
cmd/skillhub         machine CLI
internal/api         HTTP handlers (/api/admin/* admin token, /api/agent/* machine token)
internal/store       SQLite schema + queries
internal/bundle      tar.gz pack/unpack, stable digest, safe dir swap
internal/inventory   version collection (names/versions/paths only — never env or config contents)
internal/sync        client: config, lockfile, reconcile loop, symlinks, job whitelist
internal/protocol    JSON shapes shared by both sides
web/static           single-page console (vanilla JS, embedded into the binary)
deploy/              Dockerfile compose + Caddy snippet
```
