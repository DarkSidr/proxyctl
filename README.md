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

## Installer

One-command installer entrypoint (stage 11):

```bash
bash <(curl -fsSL <INSTALL_SH_URL>)
```

For local/offline setup from this repository:

```bash
sudo bash install.sh
```

Operational notes for install/update/uninstall are in `docs/INSTALLER.md`.

## Reverse proxy backend selection

`proxyctl` uses Caddy by default. To switch to Nginx, set `reverse_proxy` in `proxyctl.yaml`:

```yaml
reverse_proxy: nginx
```

To keep default behavior, use:

```yaml
reverse_proxy: caddy
```

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
