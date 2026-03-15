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

One-command installer entrypoint (stage 11):

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

Installer now asks interactively (when a TTY is available):
- reverse proxy backend (`caddy` by default),
- public domain,
- ACME contact email (for Caddy automatic TLS).
- decoy site template (`random` by default; login, pizza-club, support-desk, default).

For non-interactive provisioning you can pass values via env:

```bash
sudo PROXYCTL_PROMPT_CONFIG=0 \
  PROXYCTL_REVERSE_PROXY=caddy \
  PROXYCTL_PUBLIC_DOMAIN=darksidr.icu \
  PROXYCTL_CONTACT_EMAIL=ops@example.com \
  PROXYCTL_DECOY_TEMPLATE=random \
  bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

Installer creates decoy template library on VPS at `/usr/share/proxy-orchestrator/decoy-templates`.
You can upload your own templates there using structure: `<name>/index.html` and `<name>/assets/style.css`.

`proxyctl wizard` now includes:
- `settings -> set decoy site path`
- `settings -> switch decoy template`
- `settings -> show installed versions`
It also includes `uninstall proxyctl` for full VPS cleanup.

Reliable update/reinstall (forces source rebuild):

```bash
sudo PROXYCTL_REINSTALL_BINARY=1 PROXYCTL_INSTALL_CHANNEL=source \
  bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

Enable periodic self-update timer:

```bash
sudo PROXYCTL_ENABLE_AUTO_UPDATE=1 \
  bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
```

For local/offline setup from this repository:

```bash
sudo bash install.sh
```

Quick self-update from CLI:

```bash
sudo proxyctl update
```

`proxyctl update` first checks latest GitHub release version and skips reinstall when current version is already up to date.
After successful update it also checks `proxyctl-caddy.service` and auto-starts it when inactive (can be disabled with `--ensure-caddy=false`).

Operational notes for install/update/uninstall are in `docs/INSTALLER.md`.

Full purge command:

```bash
proxyctl uninstall --yes
```

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
sudo mkdir -p /usr/share/proxy-orchestrator/templates/decoy-site/assets
sudo curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/templates/decoy-site/index.html \
  -o /usr/share/proxy-orchestrator/templates/decoy-site/index.html
sudo curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/templates/decoy-site/assets/style.css \
  -o /usr/share/proxy-orchestrator/templates/decoy-site/assets/style.css
```

Then rebuild and apply runtime artifacts:

```bash
proxyctl render
proxyctl apply
```

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
