# Installer notes (MVP stage 11)

## One-command install

```bash
bash <(curl -fsSL <INSTALL_SH_URL>)
```

Optional environment variables:
- `PROXYCTL_BINARY_URL` — URL to `proxyctl` binary/archive (default: `https://github.com/DarkSidr/proxyctl/releases/latest/download/proxyctl-linux-amd64`).
  - If that URL returns `404`, installer automatically resolves a compatible `linux/amd64` asset from the latest GitHub release and retries.
- `PROXYCTL_REINSTALL_BINARY=1` — force overwrite of existing `/usr/local/bin/proxyctl`.
- `SINGBOX_BINARY_URL` — fallback URL for `sing-box` binary/archive.
- `XRAY_BINARY_URL` — fallback URL for `xray` binary/archive.

Supported OS (MVP):
- Debian 12
- Ubuntu 22.04
- Ubuntu 24.04

## Idempotency rules
- Existing `/etc/proxy-orchestrator/proxyctl.yaml` is not overwritten.
- Runtime defaults (`Caddyfile`, `nginx.conf`, decoy assets) are created only if missing.
- Managed systemd unit files are updated in-place with timestamped backup when content changes.
- SQLite schema init is safe for repeated runs (`CREATE TABLE IF NOT EXISTS`).

## Update notes
1. Re-run installer with a new binary URL:

```bash
sudo PROXYCTL_BINARY_URL=<new-binary-url> PROXYCTL_REINSTALL_BINARY=1 bash install.sh
```

2. Review unit file backups if installer updated units:
- `/etc/systemd/system/proxyctl-*.service.bak.<timestamp>`

3. Reload daemon and restart only required units:

```bash
sudo systemctl daemon-reload
sudo systemctl restart proxyctl-sing-box.service
sudo systemctl restart proxyctl-xray.service
sudo systemctl restart proxyctl-caddy.service
# or proxyctl-nginx.service if nginx backend is selected
```

## Uninstall notes
The project does not provide destructive auto-uninstall in MVP. Use explicit manual cleanup:

1. Stop and disable managed units:

```bash
sudo systemctl disable --now proxyctl-sing-box.service proxyctl-xray.service proxyctl-caddy.service proxyctl-nginx.service
sudo systemctl daemon-reload
```

2. Remove binaries:

```bash
sudo rm -f /usr/local/bin/proxyctl /usr/local/bin/sing-box /usr/local/bin/xray
```

3. Remove unit files:

```bash
sudo rm -f /etc/systemd/system/proxyctl-sing-box.service /etc/systemd/system/proxyctl-xray.service /etc/systemd/system/proxyctl-caddy.service /etc/systemd/system/proxyctl-nginx.service
sudo systemctl daemon-reload
```

4. Remove runtime/state/config (data loss):

```bash
sudo rm -rf /etc/proxy-orchestrator /var/lib/proxy-orchestrator /var/backups/proxy-orchestrator
```
