#!/usr/bin/env bash
set -Eeuo pipefail

IFS=$'\n\t'

INSTALL_TAG="proxyctl-installer"

readonly APP_NAME="proxyctl"
readonly CONFIG_ROOT="/etc/proxy-orchestrator"
readonly RUNTIME_ROOT="${CONFIG_ROOT}/runtime"
readonly CADDY_RUNTIME_DIR="${RUNTIME_ROOT}/caddy"
readonly NGINX_RUNTIME_DIR="${RUNTIME_ROOT}/nginx"
readonly DECOY_RUNTIME_DIR="${RUNTIME_ROOT}/decoy-site"
readonly STATE_ROOT="/var/lib/proxy-orchestrator"
readonly BACKUP_ROOT="/var/backups/proxy-orchestrator"
readonly DB_PATH="${STATE_ROOT}/proxyctl.db"
readonly CONFIG_PATH="${CONFIG_ROOT}/proxyctl.yaml"
readonly BIN_DIR="/usr/local/bin"
readonly SYSTEMD_DIR="/etc/systemd/system"
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"

PROXYCTL_BINARY_URL="${PROXYCTL_BINARY_URL:-https://raw.githubusercontent.com/DarkSidr/proxyctl/main/proxyctl}"
PROXYCTL_VERSION="${PROXYCTL_VERSION:-latest}"
PROXYCTL_REINSTALL_BINARY="${PROXYCTL_REINSTALL_BINARY:-0}"

# Optional runtime URLs for environments where apt packages are unavailable.
SINGBOX_BINARY_URL="${SINGBOX_BINARY_URL:-}"
XRAY_BINARY_URL="${XRAY_BINARY_URL:-}"

APT_UPDATED=0

log() {
  printf '[%s] %s\n' "${INSTALL_TAG}" "$*"
}

fail() {
  log "ERROR: $*"
  exit 1
}

warn() {
  log "WARN: $*"
}

on_error() {
  local line="$1"
  log "ERROR: installer failed at line ${line}"
}
trap 'on_error $LINENO' ERR

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    fail "run as root (use sudo)"
  fi
}

require_cmd() {
  local cmd="$1"
  command -v "${cmd}" >/dev/null 2>&1 || fail "required command not found: ${cmd}"
}

detect_os() {
  [[ -r /etc/os-release ]] || fail "/etc/os-release is missing"
  # shellcheck disable=SC1091
  source /etc/os-release

  local os_id="${ID:-}"
  local version_id="${VERSION_ID:-}"

  case "${os_id}:${version_id}" in
    debian:12|ubuntu:22.04|ubuntu:24.04)
      log "Detected supported OS: ${PRETTY_NAME:-${os_id} ${version_id}}"
      ;;
    *)
      fail "unsupported OS ${os_id}:${version_id}. Supported: Debian 12, Ubuntu 22.04, Ubuntu 24.04"
      ;;
  esac
}

apt_update_once() {
  if [[ "${APT_UPDATED}" -eq 0 ]]; then
    log "Updating apt index"
    DEBIAN_FRONTEND=noninteractive apt-get update -y
    APT_UPDATED=1
  fi
}

apt_install() {
  local packages=("$@")
  [[ ${#packages[@]} -gt 0 ]] || return 0
  apt_update_once
  log "Installing packages: ${packages[*]}"
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}"
}

install_first_available_pkg() {
  local cmd="$1"
  shift
  local pkg

  for pkg in "$@"; do
    if apt_install "${pkg}"; then
      if command -v "${cmd}" >/dev/null 2>&1; then
        return 0
      fi
    fi
  done

  return 1
}

download_file() {
  local url="$1"
  local out="$2"
  log "Downloading: ${url}"
  curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 10 "${url}" -o "${out}"
}

resolve_proxyctl_release_asset_url() {
  local api_url="https://api.github.com/repos/DarkSidr/proxyctl/releases/latest"
  local response

  log "Resolving proxyctl release asset URL from GitHub API" >&2
  response="$(curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 10 "${api_url}")" || return 1

  local assets
  assets="$(printf '%s\n' "${response}" \
    | grep -oE '"browser_download_url"[[:space:]]*:[[:space:]]*"[^"]+"' \
    | sed -E 's/^"browser_download_url"[[:space:]]*:[[:space:]]*"(.*)"$/\1/' \
    | sed 's#\\/#/#g')" || true

  [[ -n "${assets}" ]] || return 1

  local url
  while IFS= read -r url; do
    if [[ "${url}" =~ /proxyctl-linux-amd64$ ]]; then
      printf '%s\n' "${url}"
      return 0
    fi
  done <<<"${assets}"

  while IFS= read -r url; do
    if [[ "${url}" =~ /proxyctl([._-][0-9A-Za-z.-]+)?[-_]?linux[-_](amd64|x86_64)(\.(tar\.gz|tgz|zip))?$ ]]; then
      printf '%s\n' "${url}"
      return 0
    fi
  done <<<"${assets}"

  while IFS= read -r url; do
    if [[ "${url}" =~ /proxyctl.*(Linux|linux).*(amd64|x86_64).*(\.tar\.gz|\.tgz|\.zip)?$ ]]; then
      printf '%s\n' "${url}"
      return 0
    fi
  done <<<"${assets}"

  return 1
}

is_proxyctl_latest_download_url() {
  local url="$1"
  [[ "${url}" =~ ^https://github\.com/DarkSidr/proxyctl/releases/latest/download/ ]]
}

resolve_github_latest_asset_url() {
  local repo="$1"
  local pattern="$2"
  local api_url="https://api.github.com/repos/${repo}/releases/latest"
  local response

  log "Resolving latest release asset for ${repo}" >&2
  response="$(curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 10 "${api_url}")" || return 1

  local assets
  assets="$(printf '%s\n' "${response}" \
    | grep -oE '"browser_download_url"[[:space:]]*:[[:space:]]*"[^"]+"' \
    | sed -E 's/^"browser_download_url"[[:space:]]*:[[:space:]]*"(.*)"$/\1/' \
    | sed 's#\\/#/#g')" || true

  [[ -n "${assets}" ]] || return 1

  local url
  while IFS= read -r url; do
    if [[ "${url}" =~ ${pattern} ]]; then
      printf '%s\n' "${url}"
      return 0
    fi
  done <<<"${assets}"

  return 1
}

extract_and_install_binary() {
  local archive="$1"
  local binary_name="$2"
  local target_path="$3"

  local tmp_extract
  tmp_extract="$(mktemp -d)"
  local found=""

  case "${archive}" in
    *.tar.gz|*.tgz)
      tar -xzf "${archive}" -C "${tmp_extract}"
      found="$(find "${tmp_extract}" -type f -name "${binary_name}" | head -n1 || true)"
      ;;
    *.zip)
      unzip -q "${archive}" -d "${tmp_extract}"
      found="$(find "${tmp_extract}" -type f -name "${binary_name}" | head -n1 || true)"
      ;;
    *)
      found="${archive}"
      ;;
  esac

  [[ -n "${found}" && -f "${found}" ]] || fail "binary ${binary_name} not found in ${archive}"
  install -m 0755 "${found}" "${target_path}"
  rm -rf "${tmp_extract}"
}

write_if_absent() {
  local path="$1"
  local mode="$2"
  local content="$3"

  if [[ -e "${path}" ]]; then
    log "Keeping existing file: ${path}"
    return 0
  fi

  install -d -m 0755 "$(dirname "${path}")"
  printf '%s' "${content}" >"${path}"
  chmod "${mode}" "${path}"
  log "Created: ${path}"
}

write_managed_file() {
  local path="$1"
  local mode="$2"
  local content="$3"

  install -d -m 0755 "$(dirname "${path}")"

  if [[ -f "${path}" ]]; then
    local existing
    existing="$(cat "${path}")"
    if [[ "${existing}" == "${content}" ]]; then
      log "No changes: ${path}"
      return 0
    fi

    local backup="${path}.bak.$(date -u +%Y%m%dT%H%M%SZ)"
    cp -a "${path}" "${backup}"
    log "Backed up managed file: ${backup}"
  fi

  printf '%s' "${content}" >"${path}"
  chmod "${mode}" "${path}"
  log "Updated managed file: ${path}"
}

read_packaged_file_or_default() {
  local packaged_path="$1"
  local fallback_content="$2"

  if [[ -f "${packaged_path}" ]]; then
    cat "${packaged_path}"
    return 0
  fi

  printf '%s' "${fallback_content}"
}

install_proxyctl_binary() {
  local target="${BIN_DIR}/proxyctl"

  if [[ -x "${target}" && "${PROXYCTL_REINSTALL_BINARY}" != "1" && -z "${PROXYCTL_BINARY_URL}" ]]; then
    log "Existing proxyctl binary found at ${target}; skipping reinstall"
    return 0
  fi

  if [[ -n "${PROXYCTL_BINARY_URL}" ]]; then
    local tmpdir filename tmp source_url
    tmpdir="$(mktemp -d)"
    source_url="${PROXYCTL_BINARY_URL}"
    filename="$(basename "${source_url%%\?*}")"
    [[ -n "${filename}" ]] || filename="proxyctl.bin"
    tmp="${tmpdir}/${filename}"
    if ! download_file "${source_url}" "${tmp}"; then
      local fallback_url=""
      if is_proxyctl_latest_download_url "${source_url}"; then
        fallback_url="$(resolve_proxyctl_release_asset_url || true)"
      fi

      if [[ -n "${fallback_url}" && ! "${fallback_url}" =~ ^https?:// ]]; then
        fallback_url=""
      fi

      if [[ -n "${fallback_url}" ]]; then
        warn "Failed to download ${source_url}; trying resolved asset ${fallback_url}"
        filename="$(basename "${fallback_url%%\?*}")"
        [[ -n "${filename}" ]] || filename="proxyctl.bin"
        tmp="${tmpdir}/${filename}"
        download_file "${fallback_url}" "${tmp}"
      else
        rm -rf "${tmpdir}"
        fail "failed to download proxyctl binary from ${source_url}"
      fi
    fi
    extract_and_install_binary "${tmp}" "proxyctl" "${target}"
    rm -rf "${tmpdir}"
    log "Installed proxyctl binary from PROXYCTL_BINARY_URL"
    return 0
  fi

  local local_binary="${SCRIPT_DIR}/proxyctl"
  if [[ -x "${local_binary}" ]]; then
    install -m 0755 "${local_binary}" "${target}"
    log "Installed proxyctl binary from ${local_binary}"
    return 0
  fi

  if [[ -x "${target}" ]]; then
    log "Using existing proxyctl binary at ${target}"
    return 0
  fi

  fail "proxyctl binary source is not available. Set PROXYCTL_BINARY_URL or place executable ./proxyctl next to install.sh"
}

install_or_verify_runtime_binary() {
  local binary_name="$1"
  local env_url="$2"
  local env_var_name="$3"
  shift 3
  local package_candidates=("$@")

  if command -v "${binary_name}" >/dev/null 2>&1; then
    log "Runtime binary found: ${binary_name}"
    return 0
  fi

  if install_first_available_pkg "${binary_name}" "${package_candidates[@]}"; then
    log "Installed ${binary_name} from apt packages"
    return 0
  fi

  if [[ -z "${env_url}" ]]; then
    case "${binary_name}" in
      sing-box)
        env_url="$(resolve_github_latest_asset_url "SagerNet/sing-box" 'sing-box-.*-linux-(amd64|x86_64)\.tar\.gz$' || true)"
        ;;
      xray)
        env_url="$(resolve_github_latest_asset_url "XTLS/Xray-core" 'Xray-linux-64\.zip$' || true)"
        ;;
    esac

    if [[ -n "${env_url}" ]]; then
      log "Auto-resolved ${binary_name} binary URL: ${env_url}"
    fi
  fi

  if [[ -n "${env_url}" ]]; then
    local tmpdir filename tmp
    tmpdir="$(mktemp -d)"
    filename="$(basename "${env_url%%\?*}")"
    [[ -n "${filename}" ]] || filename="${binary_name}.bin"
    tmp="${tmpdir}/${filename}"
    download_file "${env_url}" "${tmp}"
    extract_and_install_binary "${tmp}" "${binary_name}" "${BIN_DIR}/${binary_name}"
    rm -rf "${tmpdir}"
    log "Installed ${binary_name} from URL"
    return 0
  fi

  fail "failed to install ${binary_name}. Provide ${env_var_name} or install it manually"
}

ensure_directories() {
  local dirs=(
    "${CONFIG_ROOT}"
    "${RUNTIME_ROOT}"
    "${CADDY_RUNTIME_DIR}"
    "${NGINX_RUNTIME_DIR}"
    "${DECOY_RUNTIME_DIR}"
    "${DECOY_RUNTIME_DIR}/assets"
    "${STATE_ROOT}"
    "${STATE_ROOT}/subscriptions"
    "${STATE_ROOT}/revisions"
    "${STATE_ROOT}/staging"
    "${STATE_ROOT}/logs"
    "${BACKUP_ROOT}"
  )

  local dir
  for dir in "${dirs[@]}"; do
    install -d -m 0755 "${dir}"
  done

  log "Prepared directory layout"
}

install_systemd_units() {
  local singbox_content xray_content caddy_content nginx_content

  singbox_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/systemd/proxyctl-sing-box.service" "$(cat <<'EOT'
[Unit]
Description=proxyctl sing-box runtime
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/sing-box run -D /var/lib/proxy-orchestrator -c /etc/proxy-orchestrator/runtime/sing-box.json
Restart=on-failure
RestartSec=2s
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOT
)")"

  xray_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/systemd/proxyctl-xray.service" "$(cat <<'EOT'
[Unit]
Description=proxyctl Xray runtime
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/xray run -config /etc/proxy-orchestrator/runtime/xray.json
Restart=on-failure
RestartSec=2s
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOT
)")"

  caddy_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/systemd/proxyctl-caddy.service" "$(cat <<'EOT'
[Unit]
Description=proxyctl Caddy reverse proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/caddy run --environ --config /etc/proxy-orchestrator/runtime/caddy/Caddyfile --adapter caddyfile
ExecReload=/usr/bin/caddy reload --config /etc/proxy-orchestrator/runtime/caddy/Caddyfile --adapter caddyfile
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOT
)")"

  nginx_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/systemd/proxyctl-nginx.service" "$(cat <<'EOT'
[Unit]
Description=proxyctl Nginx reverse proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=forking
ExecStartPre=/usr/sbin/nginx -t -c /etc/proxy-orchestrator/runtime/nginx/nginx.conf
ExecStart=/usr/sbin/nginx -c /etc/proxy-orchestrator/runtime/nginx/nginx.conf
ExecReload=/usr/sbin/nginx -t -c /etc/proxy-orchestrator/runtime/nginx/nginx.conf
ExecReload=/usr/sbin/nginx -s reload
ExecStop=/usr/sbin/nginx -s quit
PIDFile=/run/nginx.pid
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOT
)")"

  write_managed_file "${SYSTEMD_DIR}/proxyctl-sing-box.service" 0644 "${singbox_content}"
  write_managed_file "${SYSTEMD_DIR}/proxyctl-xray.service" 0644 "${xray_content}"
  write_managed_file "${SYSTEMD_DIR}/proxyctl-caddy.service" 0644 "${caddy_content}"
  write_managed_file "${SYSTEMD_DIR}/proxyctl-nginx.service" 0644 "${nginx_content}"

  systemctl daemon-reload
  log "Installed systemd units and reloaded daemon"
}

install_default_config() {
  local content
  content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/defaults/proxyctl.yaml" "$(cat <<'EOT'
reverse_proxy: caddy

storage:
  sqlite_path: /var/lib/proxy-orchestrator/proxyctl.db

runtime:
  singbox_unit: proxyctl-sing-box.service
  xray_unit: proxyctl-xray.service
  caddy_unit: proxyctl-caddy.service
  nginx_unit: proxyctl-nginx.service

public:
  domain: ""
  https: true
  contact_email: ""
EOT
)")"

  write_if_absent "${CONFIG_PATH}" 0640 "${content}"
}

install_default_runtime_files() {
  local caddy_content nginx_content decoy_index decoy_style

  caddy_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/defaults/runtime/caddy/Caddyfile" "$(cat <<'EOT'
:80 {
  root * /etc/proxy-orchestrator/runtime/decoy-site
  file_server
}
EOT
)")"
  nginx_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/defaults/runtime/nginx/nginx.conf" "$(cat <<'EOT'
events {}

http {
  server {
    listen 80;
    server_name _;

    root /etc/proxy-orchestrator/runtime/decoy-site;
    index index.html;

    location / {
      try_files $uri $uri/ /index.html;
    }
  }
}
EOT
)")"
  decoy_index="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/defaults/runtime/decoy-site/index.html" "$(cat <<'EOT'
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Service Portal</title>
  <link rel="stylesheet" href="/assets/style.css">
</head>
<body>
  <main class="container">
    <h1>Service Portal</h1>
    <p>This endpoint is available and serving static content.</p>
  </main>
</body>
</html>
EOT
)")"
  decoy_style="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/defaults/runtime/decoy-site/assets/style.css" "$(cat <<'EOT'
:root {
  --bg: #f2f4f6;
  --fg: #1e2933;
  --accent: #0f766e;
}

* {
  box-sizing: border-box;
}

body {
  margin: 0;
  min-height: 100vh;
  font-family: "Georgia", serif;
  color: var(--fg);
  background: radial-gradient(circle at top, #ffffff, var(--bg));
  display: grid;
  place-items: center;
}

.container {
  width: min(640px, 90vw);
  padding: 2rem;
  border: 1px solid #d8dee3;
  background: #ffffff;
  box-shadow: 0 16px 30px rgba(30, 41, 51, 0.08);
}

h1 {
  margin: 0 0 0.75rem;
  font-size: clamp(1.5rem, 3vw, 2rem);
  color: var(--accent);
}

p {
  margin: 0;
  line-height: 1.5;
}
EOT
)")"

  write_if_absent "${CADDY_RUNTIME_DIR}/Caddyfile" 0640 "${caddy_content}"
  write_if_absent "${NGINX_RUNTIME_DIR}/nginx.conf" 0640 "${nginx_content}"
  write_if_absent "${DECOY_RUNTIME_DIR}/index.html" 0644 "${decoy_index}"
  write_if_absent "${DECOY_RUNTIME_DIR}/assets/style.css" 0644 "${decoy_style}"
}

init_sqlite() {
  [[ -x "${BIN_DIR}/proxyctl" ]] || fail "proxyctl binary is not installed"
  "${BIN_DIR}/proxyctl" init --db "${DB_PATH}" >/dev/null
  chmod 0640 "${DB_PATH}" || true
  log "Initialized SQLite database: ${DB_PATH}"
}

print_next_steps() {
  cat <<'EOF_STEPS'

Installation finished.

Next steps:
1. Review configuration:
   sudo editor /etc/proxy-orchestrator/proxyctl.yaml
2. Bootstrap data model:
   sudo proxyctl node add --name primary --host <server-domain-or-ip>
   sudo proxyctl user add --name <username>
3. Add inbounds and render runtime files:
   sudo proxyctl inbound add --type vless --transport ws --node-id <node-id> --port 8443 --path /ws
   sudo proxyctl render --config /etc/proxy-orchestrator/proxyctl.yaml
4. Start only required services:
   sudo systemctl enable --now proxyctl-sing-box.service
   sudo systemctl enable --now proxyctl-caddy.service
5. Apply validated runtime update flow:
   sudo proxyctl validate --config /etc/proxy-orchestrator/proxyctl.yaml
   sudo proxyctl apply --config /etc/proxy-orchestrator/proxyctl.yaml
EOF_STEPS
}

main() {
  require_root
  require_cmd bash
  require_cmd curl
  require_cmd systemctl

  detect_os

  apt_install ca-certificates curl tar unzip xz-utils sqlite3 systemd
  apt_install caddy nginx

  install_proxyctl_binary
  install_or_verify_runtime_binary "sing-box" "${SINGBOX_BINARY_URL}" "SINGBOX_BINARY_URL" sing-box singbox
  install_or_verify_runtime_binary "xray" "${XRAY_BINARY_URL}" "XRAY_BINARY_URL" xray xray-core

  ensure_directories
  install_systemd_units
  install_default_config
  install_default_runtime_files
  init_sqlite

  log "Installed ${APP_NAME} ${PROXYCTL_VERSION} layout on host"
  print_next_steps
}

main "$@"
