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
- `PROXYCTL_PROMPT_CONFIG` — interactive setup mode (`auto` by default; set `0|false|no` to disable prompts).
- `PROXYCTL_REVERSE_PROXY` — reverse proxy backend (`caddy` or `nginx`; default: `caddy`).
- `PROXYCTL_PUBLIC_DOMAIN` — domain used for generated config/runtime defaults.
- `PROXYCTL_CONTACT_EMAIL` — ACME contact email for generated Caddy global options.
- `PROXYCTL_DECOY_TEMPLATE` — decoy site template (`random|login|pizza-club|support-desk|default`, default: `random`).
- `PROXYCTL_DECOY_TEMPLATE_BASE_URL` — base URL for downloading decoy templates when installer runs standalone via `curl|bash`.
- `PROXYCTL_AUTO_UPDATE_SCHEDULE` — `systemd` timer schedule (default: `daily`).
- `PROXYCTL_AUTO_UPDATE_INSTALL_URL` — installer URL used by auto-update script.
- `SINGBOX_BINARY_URL` — fallback URL for `sing-box` binary/archive.
- `XRAY_BINARY_URL` — fallback URL for `xray` binary/archive.
  - If runtime package is unavailable in apt, installer auto-resolves latest Linux amd64 asset from upstream GitHub release.

Supported OS (MVP):
- Debian 12
- Debian 13
- Ubuntu 22.04
- Ubuntu 24.04

Interactive behavior:
- If installer has TTY access, it asks for reverse proxy, domain, email, and decoy template choice.
- Default backend is `caddy`.
- With `caddy` + non-empty domain, installer creates HTTPS-enabled Caddy site block for that domain.
- Decoy template can be chosen explicitly or left as `random`.
- Installer creates decoy template library at `/usr/share/proxy-orchestrator/decoy-templates`.
- Custom templates can be uploaded into that directory as `<name>/index.html` and `<name>/assets/style.css`.

## Idempotency rules
- Existing `/etc/proxy-orchestrator/proxyctl.yaml` is not overwritten.
- Runtime defaults `Caddyfile`/`nginx.conf` are created only if missing.
- Decoy site assets are managed files and may be updated on reinstall when decoy template selection changes.
- Existing default `:80` Caddyfile can be upgraded to domain-based Caddyfile when `caddy` + `PROXYCTL_PUBLIC_DOMAIN` is provided.
- Managed systemd unit files are updated in-place with timestamped backup when content changes.
- SQLite schema init is safe for repeated runs (`CREATE TABLE IF NOT EXISTS`).
- Installer ensures selected reverse proxy unit is enabled/started by default, and disables conflicting proxy unit (`nginx.service` is also disabled when `caddy` is selected).

Wizard note:
- `proxyctl wizard` now has `settings -> set decoy site path` to update `paths.decoy_site_dir` in config and switch decoy assets to a custom directory.
- `proxyctl wizard` now has `settings -> switch decoy template` to activate template from `/usr/share/proxy-orchestrator/decoy-templates` (including your uploaded custom templates).
- `proxyctl wizard` now has `settings -> show installed versions` to print detected versions of `proxyctl`, `sing-box`, `xray`, `caddy`, `nginx`, `sqlite3`, and `systemd`.
- `proxyctl wizard` now has `uninstall proxyctl` for full purge flow with confirmation.

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
`proxyctl` installer now places a host uninstall script:

```bash
proxyctl uninstall --yes
# equivalent direct script call:
/usr/local/sbin/proxyctl-uninstall --yes
```

Optional removal of runtime packages:

```bash
proxyctl uninstall --yes --remove-runtime-packages
```

Manual cleanup (fallback):

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
