# proxyctl

CLI control-plane skeleton for managing proxy configuration and runtime artifacts.

## Local dev

Requirements:
- Go 1.23+

Quick checks:

```bash
make check
```

Build only:

```bash
make build
```

Run CLI help:

```bash
make smoke-help
```

## Runtime stack and versions

### Version policy (important)

`proxyctl` installer does **not** pin runtime proxy versions (`sing-box`, `xray`) in this repository.

- First it tries to install from `apt` packages (`sing-box`/`singbox`, `xray`/`xray-core`).
- If package is unavailable, it auto-resolves and installs the **latest** Linux amd64 release asset from upstream GitHub:
  - `SagerNet/sing-box`
  - `XTLS/Xray-core`
- You can force an exact build via:
  - `SINGBOX_BINARY_URL=<url>`
  - `XRAY_BINARY_URL=<url>`

Because of this, exact runtime versions depend on install date and host repository state.

### What is fixed in this repo

- Go toolchain requirement: `Go 1.23+`.
- Go module dependencies (from `go.mod`):
  - `github.com/mattn/go-sqlite3 v1.14.28`
  - `github.com/spf13/cobra v1.8.1`
  - `gopkg.in/yaml.v3 v3.0.1`
- Supported OS for installer:
  - Debian 12
  - Debian 13
  - Ubuntu 22.04
  - Ubuntu 24.04

### Check actual installed versions on a host

```bash
proxyctl --version
sing-box version
xray version
caddy version
nginx -v
```

### Current runtime versions

- `xray`: `26.2.6`
- `sing-box`: `1.13.2`
- `caddy`: `2.6.2`
- `nginx`: `1.22.1`

### Components used in proxyctl

- `proxyctl`:
  - purpose: control-plane CLI (data model, render/validate/apply pipeline, service operations).
  - version: build/release dependent (`proxyctl --version`; local repository build is often `dev`).
- `sing-box`:
  - purpose: runtime proxy engine (for example `vless`, `hysteria2` flows).
  - version now used: `1.13.2`.
- `xray`:
  - purpose: runtime proxy engine (for example `vless reality`, `xhttp` flows).
  - version now used: `26.2.6`.
- `caddy`:
  - purpose: default reverse proxy backend and TLS automation.
  - version now used: `2.6.2`.
- `nginx`:
  - purpose: optional reverse proxy backend (alternative to Caddy).
  - version now used: `1.22.1`.
- `sqlite3`:
  - purpose: local state database engine (`/var/lib/proxy-orchestrator/proxyctl.db`).
  - version: OS package dependent (check via `sqlite3 --version`).
- `systemd`:
  - purpose: service lifecycle (`proxyctl-sing-box.service`, `proxyctl-xray.service`, `proxyctl-caddy.service`, `proxyctl-nginx.service`).
  - version: OS dependent (check via `systemctl --version`).

### Go dependencies (from go.mod)

- `github.com/spf13/cobra` `v1.8.1` (CLI commands)
- `github.com/mattn/go-sqlite3` `v1.14.28` (SQLite driver)
- `gopkg.in/yaml.v3` `v3.0.1` (YAML config parsing)
- indirect:
  - `github.com/spf13/pflag` `v1.0.5`
  - `github.com/inconshreveable/mousetrap` `v1.1.0`

## Installer

Examples below are shown for running as `root` (no `sudo`).

One-command installer entrypoint (stage 11):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

Installer now asks interactively (when a TTY is available):
- deployment mode (`panel+node` by default),
- reverse proxy backend (`caddy` by default),
- public domain,
- ACME contact email (for Caddy automatic TLS).
- decoy site template (`random` by default; login, pizza-club, support-desk, default).

For non-interactive provisioning you can pass values via env:

```bash
PROXYCTL_PROMPT_CONFIG=0 \
  PROXYCTL_DEPLOYMENT_MODE=panel+node \
  PROXYCTL_REVERSE_PROXY=caddy \
  PROXYCTL_PUBLIC_DOMAIN=darksidr.icu \
  PROXYCTL_CONTACT_EMAIL=ops@example.com \
  PROXYCTL_PANEL_PATH=/my-secret-panel \
  PROXYCTL_PANEL_PORT=28443 \
  PROXYCTL_PANEL_LOGIN=admin \
  PROXYCTL_PANEL_PASSWORD='StrongPass123' \
  PROXYCTL_DECOY_TEMPLATE=random \
  bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

Installer creates decoy template library on VPS at `/usr/share/proxy-orchestrator/decoy-templates`.
You can upload your own templates there using structure: `<name>/index.html` and `<name>/assets/style.css`.
For `panel`/`panel+node` install, installer also generates panel access placeholders (3x-ui style) and stores them in `/etc/proxy-orchestrator/panel-admin.env` (path/login/password), then prints them at the end.

`proxyctl wizard` now includes:
- `settings -> set decoy site path`
- `settings -> switch decoy template`
- `settings -> show panel access info` (safe output without login/password)
- `settings -> restart panel service`
- `settings -> show installed versions`
It also includes `uninstall proxyctl` for full VPS cleanup.

Reliable update/reinstall (forces source rebuild):

```bash
PROXYCTL_REINSTALL_BINARY=1 PROXYCTL_INSTALL_CHANNEL=source \
  bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

Enable periodic self-update timer:

```bash
PROXYCTL_ENABLE_AUTO_UPDATE=1 \
  bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

For local/offline setup from this repository:

```bash
bash install.sh
```

Quick self-update from CLI:

```bash
proxyctl update
```

`proxyctl update` first checks latest GitHub release version and skips reinstall when current version is already up to date.
After successful update it also checks `proxyctl-caddy.service` and auto-starts it when inactive (can be disabled with `--ensure-caddy=false`).

Operational notes for install/update/uninstall are in `docs/INSTALLER.md`.

Full purge command:

```bash
proxyctl uninstall --yes
```

`proxyctl uninstall --yes` now also removes proxyctl-generated SSH keys from `/root/.ssh` (keys with `proxyctl-auto-` comment), proxyctl log directory, and common Caddy certificate/cache paths (`/caddy`, `/var/lib/caddy`, `/var/log/caddy`, `/etc/ssl/caddy`).
It also performs a final sweep for leftover SQLite files (`proxyctl.db`, `proxyctl.db-*`) and prints post-clean verification status for proxyctl data paths.
Additionally, uninstall tries to remove `proxyctl-auto-` entries from remote nodes' `~root/.ssh/authorized_keys` using hosts from local node DB (best-effort; other SSH keys are preserved).

Optional runtime package purge too:

```bash
proxyctl uninstall --yes --remove-runtime-packages
```

## Automated release build

GitHub Actions builds and publishes `proxyctl-linux-amd64` automatically when a tag `v*` is pushed.

Example:

```bash
git tag v0.1.1
git push origin v0.1.1
```

## Reverse proxy backend selection

`proxyctl` uses Caddy by default. To switch to Nginx, set `reverse_proxy` in `proxyctl.yaml`:

```yaml
reverse_proxy: nginx
```

To keep default behavior, use:

```yaml
reverse_proxy: caddy
```

## Decoy site template (manual refresh)

If you want to update decoy web assets from the upstream repository before rendering:

```bash
mkdir -p /usr/share/proxy-orchestrator/templates/decoy-site/assets
curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/templates/decoy-site/index.html \
  -o /usr/share/proxy-orchestrator/templates/decoy-site/index.html
curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/templates/decoy-site/assets/style.css \
  -o /usr/share/proxy-orchestrator/templates/decoy-site/assets/style.css
```

Then rebuild and apply runtime artifacts:

```bash
proxyctl render
proxyctl apply
```

## Multi-node from one panel (SSH sync)

Use one control-plane server to manage configs on multiple VPS nodes.

1. Add nodes (host must be reachable over SSH from panel host):

```bash
proxyctl node add --name eu-1 --host 203.0.113.10
proxyctl node add --name us-1 --host 198.51.100.20
proxyctl node list
```

Notes:
- only one `primary` node is allowed;
- in `proxyctl wizard -> nodes -> create node`, role `primary` is shown only when no existing primary node is present.
- after `setup ssh access`, wizard checks remote host and can install `proxyctl` there interactively (with confirmation).
- remote auto-install uses `PROXYCTL_PUBLIC_DOMAIN=<node-host>` to bootstrap TLS-ready node setup (for example for `hysteria2`).

2. Test SSH from panel host:

```bash
proxyctl node test <NODE_ID> --ssh-user root --ssh-key ~/.ssh/id_ed25519
```

3. Attach inbounds to required nodes (`--node-id <NODE_ID>`), then sync runtime configs:

```bash
proxyctl node sync --ssh-user root --ssh-key ~/.ssh/id_ed25519
```

Optional:
- only selected nodes: `proxyctl node sync --node-ids <id1,id2> ...`
- custom remote runtime dir: `--runtime-dir /etc/proxy-orchestrator/runtime`
- skip remote restart: `--restart=false`

Expected confirmation includes:
- `decoy assets: 2 files`
- `built artifacts: sing-box.json, xray.json`
- `validated artifacts: sing-box.json, xray.json`
- `service restart: proxyctl-sing-box.service`

## VLESS Reality (Xray)

To generate `vless://` links with `security=reality` and `flow=xtls-rprx-vision`, create a VLESS inbound on Xray with TCP transport:

```bash
proxyctl inbound add \
  --type vless \
  --engine xray \
  --transport tcp \
  --node-id <NODE_ID> \
  --domain darksidr.icu \
  --port 8443 \
  --sni www.intel.com \
  --reality \
  --reality-public-key <REALITY_PUBLIC_KEY> \
  --reality-private-key <REALITY_PRIVATE_KEY> \
  --reality-short-id 797e \
  --reality-fingerprint chrome \
  --reality-spider-x /Jx6iYQje4UnbubT \
  --reality-server www.intel.com \
  --reality-server-port 443 \
  --vless-flow xtls-rprx-vision
```

Notes:
- Port `443` is reserved by default for safer installs (`caddy`/site on 443). Use `--allow-port-443` only for advanced custom setups.
- If `--reality-short-id` is omitted, `proxyctl` auto-generates it.

Then regenerate subscriptions:

```bash
proxyctl subscription generate <user>
proxyctl subscription export <user> --format txt
```

`proxyctl subscription generate` now also prints a public URL in form:

```text
https://<public.domain>/sub/<token>
```

This endpoint serves plain-text URI list suitable for subscription clients (for example NekoBox).

Troubleshooting if client shows no configs:
- verify that subscription URL uses `/sub/<token>` (legacy paths are not supported);
- check endpoint manually: `curl -fsSL https://<public.domain>/sub/<token> | head`;
- some clients are sensitive to non-ASCII names in URI fragments; prefer ASCII-only client labels.

Manual and auto subscription refresh:

```bash
# One-shot refresh for one user
proxyctl subscription refresh <user>

# One-shot refresh for all users
proxyctl subscription refresh --all

# Auto refresh loop for all users (until Ctrl+C)
proxyctl subscription refresh --all --interval 10m
```

## Hysteria2 TLS (sing-box)

`hysteria2` terminates TLS in `sing-box`, so certificate paths must be present in runtime config.

`proxyctl` supports explicit TLS paths at inbound creation:

```bash
proxyctl inbound add \
  --type hysteria2 \
  --engine sing-box \
  --transport udp \
  --node-id <NODE_ID> \
  --domain darksidr.icu \
  --port 8444 \
  --tls \
  --tls-cert-path /caddy/certificates/acme-v02.api.letsencrypt.org-directory/darksidr.icu/darksidr.icu.crt \
  --tls-key-path /caddy/certificates/acme-v02.api.letsencrypt.org-directory/darksidr.icu/darksidr.icu.key
```

If `--tls-cert-path`/`--tls-key-path` are omitted, `proxyctl` auto-fills default Caddy storage paths based on inbound server name.

## XHTTP (Xray)

`xhttp` in MVP is rendered by Xray and exported as `vless://...` links with `type=xhttp`.

When `--tls` is enabled for `xhttp`, `proxyctl` now writes Xray `tlsSettings.certificates` automatically:
- if `--tls-cert-path` and `--tls-key-path` are set on inbound, those paths are used;
- otherwise defaults are auto-filled from Caddy storage using inbound server name:
  `/caddy/certificates/acme-v02.api.letsencrypt.org-directory/<server>/<server>.crt|.key`.

```bash
proxyctl inbound add \
  --type xhttp \
  --engine xray \
  --transport xhttp \
  --node-id <NODE_ID> \
  --domain darksidr.icu \
  --port 9443 \
  --tls \
  --tls-cert-path /caddy/certificates/acme-v02.api.letsencrypt.org-directory/darksidr.icu/darksidr.icu.crt \
  --tls-key-path /caddy/certificates/acme-v02.api.letsencrypt.org-directory/darksidr.icu/darksidr.icu.key \
  --path /xhttp
```

Then regenerate subscriptions:

```bash
proxyctl subscription generate <user>
proxyctl subscription export <user> --format txt
```

Interactive mode is also available:

```bash
proxyctl inbound add
```

When `--type` is not provided and stdin is a terminal, `proxyctl` starts a guided wizard for inbound creation.

Top-level wizard for common flows:

```bash
proxyctl wizard
```

The wizard includes interactive inbound setup and `update proxyctl`.
When you run just `proxyctl` in an interactive terminal, wizard starts automatically.
Wizard is inbound-first (similar to 3x-ui workflow):
- `inbounds`: list/create/open inbound, attach users to an existing inbound.
- `users`: list/create/open users, inspect configs, manage credentials.
At the end of inbound creation wizard can still print a ready client URI when a user is linked.
Inside `open credential`, you can print URI with fingerprint presets (`chrome/google`, `safari`, `firefox`, `edge`, etc.) or custom value.

## Web panel MVP (phase 0)

Read-only panel is available via:

```bash
proxyctl panel serve --config /etc/proxy-orchestrator/proxyctl.yaml
```

On installer-based hosts (`panel` / `panel+node` deployment modes), `proxyctl-panel.service` is installed and enabled automatically.
In `panel+node` mode installer auto-bootstraps `primary` node when there are no nodes yet.
In `node` mode installer auto-bootstraps `local-node` (role `node`) when there are no nodes yet.
With `caddy` + configured public domain, installer also auto-adds panel route to Caddyfile using `handle <PANEL_PATH>*` (no manual Caddy edit needed).

Defaults:
- bind: `127.0.0.1:<PANEL_PORT>` from `/etc/proxy-orchestrator/panel-admin.env`
- base path: `PANEL_PATH` from the same file
- auth: HTTP Basic Auth with `PANEL_LOGIN`/`PANEL_PASSWORD`

Current pages:
- dashboard (`runtime units`, counters),
- users list,
- inbounds list,
- subscription links list.

### Hide panel behind Caddy/Nginx (recommended)

Panel listener should stay local (`127.0.0.1`), and public access should go through reverse proxy.

Caddy example:

```caddyfile
example.com {
  handle /<secret-path>* {
    reverse_proxy 127.0.0.1:<panel-port>
  }
}
```

Nginx example:

```nginx
location /<secret-path>/ {
  proxy_pass http://127.0.0.1:<panel-port>/;
  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
  proxy_set_header X-Forwarded-Proto $scheme;
}
```

## Web panel (3x-ui/remnawave style) - incremental plan

Goal: add a lightweight visual control panel on top of existing `proxyctl` domain/app/storage layers without breaking current CLI flow.

### Phase 0 (small start, 2-4 days)

- Build read-only local web UI for one node:
  - health cards (`status`, services, db path),
  - users table,
  - inbounds table,
  - generated subscription links view.
- Keep business logic in existing app/service layer; UI calls internal HTTP handlers only.
- Access model for MVP:
  - bind to `127.0.0.1:<port>` only,
  - optional reverse-proxy exposure later.
- Deliverable:
  - `/panel` route with basic dashboard and list pages.

### Phase 1 (safe write operations)

- Add create/edit/delete for users and inbounds from UI.
- Add explicit action buttons for `render`, `validate`, `apply`.
- Add operation result feed (success/error log for latest action).
- Add optimistic locking/version check for entities to avoid accidental overwrite.

### Phase 2 (node operations + observability)

- Multi-node page: node list, reachability test, sync trigger per node.
- Runtime/logs page: tail recent service logs, filter by unit.
- Diagnostics page: expose `doctor` checks with pass/fail badges and suggested fixes.

### Phase 3 (production-grade panel)

- AuthN/AuthZ:
  - admin login,
  - optional API tokens for automation,
  - role split (read-only/operator/admin).
- Audit trail for all mutating actions (who, when, before/after).
- Safer rollout UX:
  - "preview diff" before apply,
  - staged apply per node,
  - rollback shortcuts for last known good runtime artifacts.

### Technical constraints and architecture notes

- Do not duplicate validation logic in frontend; server remains source of truth.
- Keep CLI and panel parity: any panel action should map to existing use-cases in `internal/app`.
- Start with server-rendered HTML + minimal JS; SPA is optional later.
- Prefer SQLite-first for panel metadata; postpone external DB until needed.

### Definition of done for Phase 0

- Read-only dashboard works on fresh install.
- All data shown in panel is sourced via existing repositories/services (no duplicated storage model).
- Basic smoke test is added to `TESTING.md` for panel route availability.

## Troubleshooting

Quick operational diagnostics:

```bash
# High-level runtime/db/service snapshot
proxyctl status

# Typical operational checks (returns non-zero on blocking issues)
proxyctl doctor
```

Inspect logs from relevant units:

```bash
# Runtime services (sing-box/xray + selected reverse proxy)
proxyctl logs --lines 200
proxyctl logs runtime --since "1 hour ago"

# Follow runtime logs
proxyctl logs -f

# Apply pipeline logs (if proxyctl-apply.service is used)
proxyctl logs apply --lines 200
```

Typical `doctor` findings and actions:
- `database file is missing`:
  - run `proxyctl init --db /var/lib/proxy-orchestrator/proxyctl.db`
- `database is not initialized`:
  - run `proxyctl init` with the same DB path used by your config
- `reverse proxy config is missing`:
  - run `proxyctl render` or `proxyctl apply`
- `no users found`:
  - create at least one user (`proxyctl user add --name <name>`)
- `runtime-file ... missing or invalid`:
  - regenerate runtime artifacts (`proxyctl render`) and re-check permissions/paths

## Manual smoke testing

Use [`TESTING.md`](TESTING.md) for the full manual checklist:
- install
- init
- add user
- add inbound (`vless`, `hysteria2`, `xhttp`)
- generate subscription
- validate
- apply
- status
- logs
