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
sudo bash <(curl -fsSL https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh)
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
  --port 443 \
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

Then regenerate subscriptions:

```bash
proxyctl subscription generate <user>
proxyctl subscription export <user> --format txt
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
