# Installer notes (MVP stage 11)

## One-command install

```bash
bash <(curl -fsSL <INSTALL_SH_URL>)
```

Optional environment variables:
- `PROXYCTL_INSTALL_CHANNEL` — install strategy for `proxyctl` binary:
  - `auto` (default): `release -> source build -> local ./proxyctl -> main branch binary URL`.
  - `release`: only GitHub release asset (`linux/amd64` resolved via GitHub API).
  - `source`: build from source archive (`go build ./cmd/proxyctl`).
  - `url`: only `PROXYCTL_BINARY_URL`.
  - `local`: only local `./proxyctl` next to `install.sh`.
- `PROXYCTL_BINARY_URL` — URL to `proxyctl` binary/archive (optional override).
- `PROXYCTL_SOURCE_ARCHIVE_URL` — source tarball URL for `source`/`auto` modes (default: `https://codeload.github.com/DarkSidr/proxyctl/tar.gz/refs/heads/main`).
- `PROXYCTL_MAIN_BINARY_URL` — fallback raw main-branch binary URL used by `auto`.
- `PROXYCTL_REINSTALL_BINARY=1` — force overwrite of existing `/usr/local/bin/proxyctl`.
- `PROXYCTL_ENABLE_AUTO_UPDATE=1` — install and enable `proxyctl-self-update.timer`.
- `PROXYCTL_ENABLE_CADDY_ON_INSTALL=1` — ensure `proxyctl-caddy.service` is enabled and started during install (default: `1`; set `0` to skip).
- `PROXYCTL_AUTO_UPDATE_SCHEDULE` — `systemd` timer schedule (default: `daily`).
- `PROXYCTL_AUTO_UPDATE_INSTALL_URL` — installer URL used by auto-update script.
- `SINGBOX_BINARY_URL` — fallback URL for `sing-box` binary/archive.
- `XRAY_BINARY_URL` — fallback URL for `xray` binary/archive.
  - If runtime package is unavailable in apt, installer auto-resolves latest Linux amd64 asset from upstream GitHub release.

Supported OS (MVP):
- Debian 12
- Ubuntu 22.04
- Ubuntu 24.04

## Idempotency rules
- Existing `/etc/proxy-orchestrator/proxyctl.yaml` is not overwritten.
- Runtime defaults (`Caddyfile`, `nginx.conf`, decoy assets) are created only if missing.
- Managed systemd unit files are updated in-place with timestamped backup when content changes.
- SQLite schema init is safe for repeated runs (`CREATE TABLE IF NOT EXISTS`).
- Installer ensures `proxyctl-caddy.service` is enabled/started by default (`PROXYCTL_ENABLE_CADDY_ON_INSTALL=1`).

## Update notes
1. Re-run installer (recommended strategy: source build when release assets are missing):

```bash
sudo PROXYCTL_REINSTALL_BINARY=1 PROXYCTL_INSTALL_CHANNEL=source bash install.sh
```

or with explicit URL:

```bash
sudo PROXYCTL_REINSTALL_BINARY=1 PROXYCTL_INSTALL_CHANNEL=url PROXYCTL_BINARY_URL=<new-binary-url> bash install.sh
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

CLI self-update notes:
- `proxyctl update` now validates caddy service state after update and auto-starts `proxyctl-caddy.service` when inactive.
- Disable this behavior for advanced setups with `proxyctl update --ensure-caddy=false`.

## Auto-update timer

Install/update and enable periodic self-update:

```bash
sudo PROXYCTL_ENABLE_AUTO_UPDATE=1 PROXYCTL_REINSTALL_BINARY=1 bash install.sh
sudo systemctl list-timers proxyctl-self-update.timer
```

Run once manually:

```bash
sudo systemctl start proxyctl-self-update.service
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
