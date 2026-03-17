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
readonly SHARE_ROOT="/usr/share/proxy-orchestrator"
readonly SHARE_TEMPLATES_DIR="${SHARE_ROOT}/templates"
readonly SHARE_ACTIVE_DECOY_DIR="${SHARE_TEMPLATES_DIR}/decoy-site"
readonly SHARE_DECOY_LIBRARY_DIR="${SHARE_ROOT}/decoy-templates"
readonly STATE_ROOT="/var/lib/proxy-orchestrator"
readonly BACKUP_ROOT="/var/backups/proxy-orchestrator"
readonly DB_PATH="${STATE_ROOT}/proxyctl.db"
readonly CONFIG_PATH="${CONFIG_ROOT}/proxyctl.yaml"
readonly PANEL_CREDENTIALS_PATH="${CONFIG_ROOT}/panel-admin.env"
readonly BIN_DIR="/usr/local/bin"
readonly SBIN_DIR="/usr/local/sbin"
readonly SYSTEMD_DIR="/etc/systemd/system"
SCRIPT_SOURCE="${BASH_SOURCE[0]-$0}"
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${SCRIPT_SOURCE}")" >/dev/null 2>&1 && pwd -P)"

PROXYCTL_BINARY_URL="${PROXYCTL_BINARY_URL:-}"
PROXYCTL_INSTALL_CHANNEL="${PROXYCTL_INSTALL_CHANNEL:-auto}"
PROXYCTL_VERSION="${PROXYCTL_VERSION:-latest}"
PROXYCTL_REINSTALL_BINARY="${PROXYCTL_REINSTALL_BINARY:-0}"
PROXYCTL_SOURCE_ARCHIVE_URL="${PROXYCTL_SOURCE_ARCHIVE_URL:-https://codeload.github.com/DarkSidr/proxyctl/tar.gz/refs/heads/main}"
PROXYCTL_MAIN_BINARY_URL="${PROXYCTL_MAIN_BINARY_URL:-https://raw.githubusercontent.com/DarkSidr/proxyctl/main/proxyctl}"
PROXYCTL_ENABLE_AUTO_UPDATE="${PROXYCTL_ENABLE_AUTO_UPDATE:-0}"
PROXYCTL_ENABLE_CADDY_ON_INSTALL="${PROXYCTL_ENABLE_CADDY_ON_INSTALL:-1}"
PROXYCTL_AUTO_UPDATE_SCHEDULE="${PROXYCTL_AUTO_UPDATE_SCHEDULE:-daily}"
PROXYCTL_AUTO_UPDATE_INSTALL_URL="${PROXYCTL_AUTO_UPDATE_INSTALL_URL:-https://raw.githubusercontent.com/DarkSidr/proxyctl/main/install.sh}"
PROXYCTL_DEPLOYMENT_MODE="${PROXYCTL_DEPLOYMENT_MODE:-}"
PROXYCTL_REVERSE_PROXY="${PROXYCTL_REVERSE_PROXY:-}"
PROXYCTL_PUBLIC_DOMAIN="${PROXYCTL_PUBLIC_DOMAIN:-}"
PROXYCTL_CONTACT_EMAIL="${PROXYCTL_CONTACT_EMAIL:-}"
PROXYCTL_PROMPT_CONFIG="${PROXYCTL_PROMPT_CONFIG:-auto}"
PROXYCTL_DECOY_TEMPLATE="${PROXYCTL_DECOY_TEMPLATE:-random}"
PROXYCTL_DECOY_TEMPLATE_BASE_URL="${PROXYCTL_DECOY_TEMPLATE_BASE_URL:-https://raw.githubusercontent.com/DarkSidr/proxyctl/main/packaging/defaults/decoy-templates}"
PROXYCTL_PANEL_PATH="${PROXYCTL_PANEL_PATH:-}"
PROXYCTL_PANEL_LOGIN="${PROXYCTL_PANEL_LOGIN:-}"
PROXYCTL_PANEL_PASSWORD="${PROXYCTL_PANEL_PASSWORD:-}"
PROXYCTL_PANEL_PORT="${PROXYCTL_PANEL_PORT:-}"

# Optional runtime URLs for environments where apt packages are unavailable.
SINGBOX_BINARY_URL="${SINGBOX_BINARY_URL:-}"
XRAY_BINARY_URL="${XRAY_BINARY_URL:-}"

APT_UPDATED=0
SELECTED_DEPLOYMENT_MODE="panel+node"
SELECTED_REVERSE_PROXY="caddy"
SELECTED_PUBLIC_DOMAIN=""
SELECTED_CONTACT_EMAIL=""
SELECTED_DECOY_TEMPLATE="random"
DECOY_INDEX_CONTENT=""
DECOY_STYLE_CONTENT=""
SELECTED_PANEL_PATH=""
SELECTED_PANEL_LOGIN=""
SELECTED_PANEL_PASSWORD=""
SELECTED_PANEL_PORT=""
PANEL_URL_HINT=""

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

can_prompt() {
  local mode
  mode="$(printf '%s' "${PROXYCTL_PROMPT_CONFIG}" | tr '[:upper:]' '[:lower:]')"
  if [[ "${mode}" == "0" || "${mode}" == "false" || "${mode}" == "no" ]]; then
    return 1
  fi
  [[ -t 0 ]]
}

prompt_with_default() {
  local label="$1"
  local default_value="$2"
  local answer=""
  local prompt="${label}"

  if [[ -n "${default_value}" ]]; then
    prompt+=" [${default_value}]"
  fi
  prompt+=": "

  read -r -p "${prompt}" answer || true
  if [[ -z "${answer}" ]]; then
    printf '%s\n' "${default_value}"
  else
    printf '%s\n' "${answer}"
  fi
}

prompt_reverse_proxy_choice() {
  local current="$1"
  local prompt_target="/dev/stderr"
  if [[ -w /dev/tty ]]; then
    prompt_target="/dev/tty"
  fi
  local default_choice="1"
  case "${current}" in
    caddy) default_choice="1" ;;
    nginx) default_choice="2" ;;
  esac
  local answer=""
  local normalized
  while true; do
    printf 'Reverse proxy:\n' >"${prompt_target}"
    printf '  1) caddy\n' >"${prompt_target}"
    printf '  2) nginx\n' >"${prompt_target}"
    printf 'Select reverse proxy [%s]: ' "${default_choice}" >"${prompt_target}"
    if [[ -r /dev/tty ]]; then
      IFS= read -r answer < /dev/tty || true
    else
      IFS= read -r answer || true
    fi
    answer="${answer:-${default_choice}}"
    normalized="$(printf '%s' "${answer}" | tr '[:upper:]' '[:lower:]')"
    case "${normalized}" in
      1|caddy)
        printf '%s\n' "caddy"
        return 0
        ;;
      2|nginx)
        printf '%s\n' "nginx"
        return 0
        ;;
      *)
        warn "Unsupported reverse proxy choice: ${answer}. Use 1/2 or caddy/nginx."
        ;;
    esac
  done
}

prompt_deployment_mode_choice() {
  local current="$1"
  local prompt_target="/dev/stderr"
  if [[ -w /dev/tty ]]; then
    prompt_target="/dev/tty"
  fi
  local default_choice="1"
  case "${current}" in
    panel+node) default_choice="1" ;;
    panel) default_choice="2" ;;
    node) default_choice="3" ;;
  esac
  local answer=""
  local normalized
  while true; do
    printf 'Deployment mode:\n' >"${prompt_target}"
    printf '  1) panel+node\n' >"${prompt_target}"
    printf '  2) panel\n' >"${prompt_target}"
    printf '  3) node\n' >"${prompt_target}"
    printf 'Select deployment mode [%s]: ' "${default_choice}" >"${prompt_target}"
    if [[ -r /dev/tty ]]; then
      IFS= read -r answer < /dev/tty || true
    else
      IFS= read -r answer || true
    fi
    answer="${answer:-${default_choice}}"
    normalized="$(printf '%s' "${answer}" | tr '[:upper:]' '[:lower:]')"
    case "${normalized}" in
      1|panel+node)
        printf '%s\n' "panel+node"
        return 0
        ;;
      2|panel)
        printf '%s\n' "panel"
        return 0
        ;;
      3|node)
        printf '%s\n' "node"
        return 0
        ;;
      *)
        warn "Unsupported deployment mode: ${answer}. Use 1/2/3 or panel+node/panel/node."
        ;;
    esac
  done
}

prompt_decoy_template_choice() {
  local current="$1"
  local prompt_target="/dev/stderr"
  if [[ -w /dev/tty ]]; then
    prompt_target="/dev/tty"
  fi
  local default_choice="1"
  case "${current}" in
    random) default_choice="1" ;;
    login) default_choice="2" ;;
    pizza-club) default_choice="3" ;;
    support-desk) default_choice="4" ;;
    default) default_choice="5" ;;
  esac
  local answer=""
  local normalized
  while true; do
    printf 'Decoy site template:\n' >"${prompt_target}"
    printf '  1) random\n' >"${prompt_target}"
    printf '  2) login\n' >"${prompt_target}"
    printf '  3) pizza-club\n' >"${prompt_target}"
    printf '  4) support-desk\n' >"${prompt_target}"
    printf '  5) default\n' >"${prompt_target}"
    printf 'Select decoy template [%s]: ' "${default_choice}" >"${prompt_target}"
    if [[ -r /dev/tty ]]; then
      IFS= read -r answer < /dev/tty || true
    else
      IFS= read -r answer || true
    fi
    answer="${answer:-${default_choice}}"
    normalized="$(printf '%s' "${answer}" | tr '[:upper:]' '[:lower:]')"
    case "${normalized}" in
      1|random)
        printf '%s\n' "random"
        return 0
        ;;
      2|login)
        printf '%s\n' "login"
        return 0
        ;;
      3|pizza-club)
        printf '%s\n' "pizza-club"
        return 0
        ;;
      4|support-desk)
        printf '%s\n' "support-desk"
        return 0
        ;;
      5|default)
        printf '%s\n' "default"
        return 0
        ;;
      *)
        warn "Unsupported decoy template: ${answer}. Use 1..5 or random/login/pizza-club/support-desk/default."
        ;;
    esac
  done
}

resolve_decoy_template_choice() {
  local requested="$1"
  local normalized
  normalized="$(printf '%s' "${requested}" | tr '[:upper:]' '[:lower:]')"
  if [[ "${normalized}" != "random" && "${normalized}" != "login" && "${normalized}" != "pizza-club" && "${normalized}" != "support-desk" && "${normalized}" != "default" ]]; then
    normalized="random"
  fi
  if [[ "${normalized}" == "random" ]]; then
    local choices=("login" "pizza-club" "support-desk")
    local idx=$((RANDOM % ${#choices[@]}))
    printf '%s\n' "${choices[${idx}]}"
    return 0
  fi
  printf '%s\n' "${normalized}"
}

random_alnum() {
  local length="$1"
  local output=""
  while [[ ${#output} -lt ${length} ]]; do
    output+="$(
      LC_ALL=C tr -dc 'a-zA-Z0-9' </dev/urandom | head -c "${length}" || true
    )"
  done
  printf '%s\n' "${output:0:length}"
}

random_panel_port() {
  local min=20000
  local max=65000
  local range=$((max - min + 1))
  printf '%s\n' "$((min + (RANDOM % range)))"
}

normalize_panel_path() {
  local raw="$1"
  local trimmed sanitized

  trimmed="$(printf '%s' "${raw}" | xargs || true)"
  sanitized="$(printf '%s' "${trimmed}" | tr -cd 'a-zA-Z0-9/_-')"
  sanitized="${sanitized#/}"

  if [[ -z "${sanitized}" ]]; then
    printf '%s\n' ""
    return 0
  fi
  if [[ "${sanitized}" =~ ^- ]]; then
    sanitized="panel-${sanitized#-}"
  fi
  if [[ "${sanitized}" == "/" ]]; then
    printf '/panel-%s\n' "$(random_alnum 8 | tr '[:upper:]' '[:lower:]')"
    return 0
  fi
  printf '/%s\n' "${sanitized}"
}

read_panel_cred_field() {
  local key="$1"
  local file_path="$2"
  awk -F'=' -v wanted="${key}" '$1 == wanted {print substr($0, index($0, "=") + 1); exit}' "${file_path}" 2>/dev/null || true
}

is_valid_port() {
  local value="$1"
  [[ "${value}" =~ ^[0-9]+$ ]] || return 1
  ((value >= 1 && value <= 65535))
}

prompt_panel_port() {
  local current="$1"
  local answer=""
  while true; do
    answer="$(prompt_with_default "Panel port (1-65535)" "${current}")"
    answer="$(printf '%s' "${answer}" | xargs || true)"
    if is_valid_port "${answer}"; then
      printf '%s\n' "${answer}"
      return 0
    fi
    warn "Unsupported panel port: ${answer}. Use numeric value in range 1..65535."
  done
}

prepare_panel_credentials() {
  local existing_path="" existing_login="" existing_password="" existing_port="" resolved_path=""

  if [[ "${SELECTED_DEPLOYMENT_MODE}" == "node" ]]; then
    SELECTED_PANEL_PATH=""
    SELECTED_PANEL_LOGIN=""
    SELECTED_PANEL_PASSWORD=""
    PANEL_URL_HINT=""
    return 0
  fi

  if [[ -f "${PANEL_CREDENTIALS_PATH}" ]]; then
    existing_path="$(read_panel_cred_field "PANEL_PATH" "${PANEL_CREDENTIALS_PATH}")"
    existing_login="$(read_panel_cred_field "PANEL_LOGIN" "${PANEL_CREDENTIALS_PATH}")"
    existing_password="$(read_panel_cred_field "PANEL_PASSWORD" "${PANEL_CREDENTIALS_PATH}")"
    existing_port="$(read_panel_cred_field "PANEL_PORT" "${PANEL_CREDENTIALS_PATH}")"
  fi

  resolved_path="$(normalize_panel_path "${PROXYCTL_PANEL_PATH}")"
  if [[ -z "${resolved_path}" ]]; then
    resolved_path="$(normalize_panel_path "${existing_path}")"
  fi
  if [[ -z "${resolved_path}" ]]; then
    resolved_path="/$(random_alnum 16 | tr '[:upper:]' '[:lower:]')"
  fi

  SELECTED_PANEL_PATH="${resolved_path}"
  SELECTED_PANEL_LOGIN="${PROXYCTL_PANEL_LOGIN:-${existing_login}}"
  SELECTED_PANEL_PASSWORD="${PROXYCTL_PANEL_PASSWORD:-${existing_password}}"
  SELECTED_PANEL_PORT="${PROXYCTL_PANEL_PORT:-${existing_port}}"

  if [[ -z "${SELECTED_PANEL_LOGIN}" ]]; then
    SELECTED_PANEL_LOGIN="admin"
  fi
  if [[ -z "${SELECTED_PANEL_PASSWORD}" ]]; then
    SELECTED_PANEL_PASSWORD="$(random_alnum 20)"
  fi
  if ! is_valid_port "${SELECTED_PANEL_PORT}"; then
    SELECTED_PANEL_PORT="$(random_panel_port)"
  fi

  if can_prompt; then
    SELECTED_PANEL_PORT="$(prompt_panel_port "${SELECTED_PANEL_PORT}")"
  fi

  if [[ -n "${SELECTED_PUBLIC_DOMAIN}" ]]; then
    if [[ "${SELECTED_REVERSE_PROXY}" == "caddy" ]]; then
      PANEL_URL_HINT="https://${SELECTED_PUBLIC_DOMAIN}${SELECTED_PANEL_PATH}"
    else
      PANEL_URL_HINT="http://${SELECTED_PUBLIC_DOMAIN}${SELECTED_PANEL_PATH}"
    fi
  else
    PANEL_URL_HINT="http://<server-ip-or-domain>:${SELECTED_PANEL_PORT}${SELECTED_PANEL_PATH}"
  fi
}

write_panel_credentials() {
  if [[ "${SELECTED_DEPLOYMENT_MODE}" == "node" ]]; then
    return 0
  fi
  local content
  content="$(cat <<EOT
# Generated by proxyctl installer.
# Future web panel credentials/path placeholder (3x-ui style flow).
PANEL_PATH=${SELECTED_PANEL_PATH}
PANEL_LOGIN=${SELECTED_PANEL_LOGIN}
PANEL_PASSWORD=${SELECTED_PANEL_PASSWORD}
PANEL_PORT=${SELECTED_PANEL_PORT}
EOT
)"
  write_managed_file "${PANEL_CREDENTIALS_PATH}" 0600 "${content}"
}

build_decoy_assets() {
  local requested="$1"
  local resolved src_base index_path style_path
  resolved="$(resolve_decoy_template_choice "${requested}")"
  SELECTED_DECOY_TEMPLATE="${resolved}"

  src_base="${SHARE_DECOY_LIBRARY_DIR}/${resolved}"
  index_path="${src_base}/index.html"
  style_path="${src_base}/assets/style.css"
  if [[ ! -f "${index_path}" || ! -f "${style_path}" ]]; then
    warn "Decoy template ${resolved} is missing in ${SHARE_DECOY_LIBRARY_DIR}; falling back to default"
    resolved="default"
    SELECTED_DECOY_TEMPLATE="${resolved}"
    src_base="${SHARE_DECOY_LIBRARY_DIR}/${resolved}"
    index_path="${src_base}/index.html"
    style_path="${src_base}/assets/style.css"
  fi

  [[ -f "${index_path}" ]] || fail "missing decoy template index: ${index_path}"
  [[ -f "${style_path}" ]] || fail "missing decoy template style: ${style_path}"
  DECOY_INDEX_CONTENT="$(cat "${index_path}")"
  DECOY_STYLE_CONTENT="$(cat "${style_path}")"
}

read_decoy_template_file() {
  local template_name="$1"
  local rel_path="$2"
  local packaged="${SCRIPT_DIR}/packaging/defaults/decoy-templates/${template_name}/${rel_path}"
  local remote="${PROXYCTL_DECOY_TEMPLATE_BASE_URL}/${template_name}/${rel_path}"
  local content=""

  if [[ -f "${packaged}" ]]; then
    cat "${packaged}"
    return 0
  fi
  if content="$(curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 10 "${remote}" 2>/dev/null)"; then
    printf '%s' "${content}"
    return 0
  fi
  return 1
}

install_decoy_template_library() {
  local templates=("default" "login" "pizza-club" "support-desk")
  local template_name index style

  install -d -m 0755 "${SHARE_DECOY_LIBRARY_DIR}" "${SHARE_ACTIVE_DECOY_DIR}/assets"

  for template_name in "${templates[@]}"; do
    index="$(read_decoy_template_file "${template_name}" "index.html" || true)"
    style="$(read_decoy_template_file "${template_name}" "assets/style.css" || true)"
    if [[ -z "${index}" || -z "${style}" ]]; then
      fail "failed to load decoy template files for ${template_name}"
    fi
    write_managed_file "${SHARE_DECOY_LIBRARY_DIR}/${template_name}/index.html" 0644 "${index}"
    write_managed_file "${SHARE_DECOY_LIBRARY_DIR}/${template_name}/assets/style.css" 0644 "${style}"
  done
  log "Installed decoy template library: ${SHARE_DECOY_LIBRARY_DIR}"
}

activate_decoy_template() {
  local index_content="$1"
  local style_content="$2"
  write_managed_file "${DECOY_RUNTIME_DIR}/index.html" 0644 "${index_content}"
  write_managed_file "${DECOY_RUNTIME_DIR}/assets/style.css" 0644 "${style_content}"
  write_managed_file "${SHARE_ACTIVE_DECOY_DIR}/index.html" 0644 "${index_content}"
  write_managed_file "${SHARE_ACTIVE_DECOY_DIR}/assets/style.css" 0644 "${style_content}"
}

configure_install_preferences() {
  local existing_deployment_mode="panel+node"
  local existing_reverse_proxy="caddy"
  local existing_domain=""
  local existing_email=""
  local prompt_enabled="0"

  if [[ -f "${CONFIG_PATH}" ]]; then
    existing_deployment_mode="$(awk -F':' '/^[[:space:]]*deployment_mode:[[:space:]]*/ {gsub(/[[:space:]]/, "", $2); print tolower($2); exit}' "${CONFIG_PATH}" || true)"
    existing_reverse_proxy="$(awk -F':' '/^[[:space:]]*reverse_proxy:[[:space:]]*/ {gsub(/[[:space:]]/, "", $2); print tolower($2); exit}' "${CONFIG_PATH}" || true)"
    existing_domain="$(awk -F':' '/^[[:space:]]*domain:[[:space:]]*/ {sub(/^[[:space:]]*/, "", $2); gsub(/^"|"$/, "", $2); print $2; exit}' "${CONFIG_PATH}" || true)"
    existing_email="$(awk -F':' '/^[[:space:]]*contact_email:[[:space:]]*/ {sub(/^[[:space:]]*/, "", $2); gsub(/^"|"$/, "", $2); print $2; exit}' "${CONFIG_PATH}" || true)"
  fi
  if [[ "${existing_reverse_proxy}" != "nginx" ]]; then
    existing_reverse_proxy="caddy"
  fi

  SELECTED_DEPLOYMENT_MODE="${existing_deployment_mode}"
  SELECTED_REVERSE_PROXY="${existing_reverse_proxy}"
  SELECTED_PUBLIC_DOMAIN="${existing_domain}"
  SELECTED_CONTACT_EMAIL="${existing_email}"

  if [[ -n "${PROXYCTL_DEPLOYMENT_MODE}" ]]; then
    SELECTED_DEPLOYMENT_MODE="$(printf '%s' "${PROXYCTL_DEPLOYMENT_MODE}" | tr '[:upper:]' '[:lower:]')"
  fi
  if [[ "${SELECTED_DEPLOYMENT_MODE}" != "panel+node" && "${SELECTED_DEPLOYMENT_MODE}" != "panel" && "${SELECTED_DEPLOYMENT_MODE}" != "node" ]]; then
    warn "Invalid deployment mode '${SELECTED_DEPLOYMENT_MODE}'; falling back to panel+node"
    SELECTED_DEPLOYMENT_MODE="panel+node"
  fi

  if [[ -n "${PROXYCTL_REVERSE_PROXY}" ]]; then
    SELECTED_REVERSE_PROXY="$(printf '%s' "${PROXYCTL_REVERSE_PROXY}" | tr '[:upper:]' '[:lower:]')"
  fi
  if [[ "${SELECTED_REVERSE_PROXY}" != "caddy" && "${SELECTED_REVERSE_PROXY}" != "nginx" ]]; then
    warn "Invalid PROXYCTL_REVERSE_PROXY=${PROXYCTL_REVERSE_PROXY}; falling back to caddy"
    SELECTED_REVERSE_PROXY="caddy"
  fi

  if [[ -n "${PROXYCTL_PUBLIC_DOMAIN}" ]]; then
    SELECTED_PUBLIC_DOMAIN="${PROXYCTL_PUBLIC_DOMAIN}"
  fi
  if [[ -n "${PROXYCTL_CONTACT_EMAIL}" ]]; then
    SELECTED_CONTACT_EMAIL="${PROXYCTL_CONTACT_EMAIL}"
  fi
  if [[ -n "${PROXYCTL_DECOY_TEMPLATE}" ]]; then
    SELECTED_DECOY_TEMPLATE="$(printf '%s' "${PROXYCTL_DECOY_TEMPLATE}" | tr '[:upper:]' '[:lower:]')"
  fi

  if can_prompt; then
    prompt_enabled="1"
  fi

  if [[ "${prompt_enabled}" == "1" ]]; then
    log "Interactive setup: deployment mode, reverse proxy and public endpoint settings"
    SELECTED_DEPLOYMENT_MODE="$(prompt_deployment_mode_choice "${SELECTED_DEPLOYMENT_MODE}")"
    SELECTED_REVERSE_PROXY="$(prompt_reverse_proxy_choice "${SELECTED_REVERSE_PROXY}")"
    if [[ "${SELECTED_REVERSE_PROXY}" == "caddy" ]]; then
      SELECTED_PUBLIC_DOMAIN="$(prompt_with_default "Public domain (required for automatic HTTPS)" "${SELECTED_PUBLIC_DOMAIN}")"
      SELECTED_CONTACT_EMAIL="$(prompt_with_default "ACME contact email (optional but recommended)" "${SELECTED_CONTACT_EMAIL}")"
    else
      SELECTED_PUBLIC_DOMAIN="$(prompt_with_default "Public domain (optional for subscriptions)" "${SELECTED_PUBLIC_DOMAIN}")"
    fi
    SELECTED_DECOY_TEMPLATE="$(prompt_decoy_template_choice "${SELECTED_DECOY_TEMPLATE}")"
  fi

  SELECTED_PUBLIC_DOMAIN="$(printf '%s' "${SELECTED_PUBLIC_DOMAIN}" | xargs || true)"
  SELECTED_CONTACT_EMAIL="$(printf '%s' "${SELECTED_CONTACT_EMAIL}" | xargs || true)"
  SELECTED_DECOY_TEMPLATE="$(resolve_decoy_template_choice "${SELECTED_DECOY_TEMPLATE}")"
  log "Selected deployment mode: ${SELECTED_DEPLOYMENT_MODE}"
  log "Selected reverse proxy: ${SELECTED_REVERSE_PROXY}"
  if [[ -n "${SELECTED_PUBLIC_DOMAIN}" ]]; then
    log "Selected public domain: ${SELECTED_PUBLIC_DOMAIN}"
  fi
  log "Selected decoy template: ${SELECTED_DECOY_TEMPLATE}"
}

detect_os() {
  [[ -r /etc/os-release ]] || fail "/etc/os-release is missing"
  # shellcheck disable=SC1091
  source /etc/os-release

  local os_id="${ID:-}"
  local version_id="${VERSION_ID:-}"

  case "${os_id}:${version_id}" in
    debian:12|debian:13|ubuntu:22.04|ubuntu:24.04)
      log "Detected supported OS: ${PRETTY_NAME:-${os_id} ${version_id}}"
      ;;
    *)
      fail "unsupported OS ${os_id}:${version_id}. Supported: Debian 12, Debian 13, Ubuntu 22.04, Ubuntu 24.04"
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

  if [[ -z "${found}" || ! -f "${found}" ]]; then
    rm -rf "${tmp_extract}"
    return 1
  fi
  install -m 0755 "${found}" "${target_path}"
  rm -rf "${tmp_extract}"
}

install_proxyctl_from_url() {
  local source_url="$1"
  local target="$2"
  local tmpdir filename tmp

  tmpdir="$(mktemp -d)"
  filename="$(basename "${source_url%%\?*}")"
  [[ -n "${filename}" ]] || filename="proxyctl.bin"
  tmp="${tmpdir}/${filename}"

  if ! download_file "${source_url}" "${tmp}"; then
    rm -rf "${tmpdir}"
    return 1
  fi
  if ! extract_and_install_binary "${tmp}" "proxyctl" "${target}"; then
    rm -rf "${tmpdir}"
    return 1
  fi

  rm -rf "${tmpdir}"
  return 0
}

install_proxyctl_from_release() {
  local target="$1"
  local url

  url="$(resolve_proxyctl_release_asset_url || true)"
  if [[ -z "${url}" ]]; then
    return 1
  fi

  if ! install_proxyctl_from_url "${url}" "${target}"; then
    return 1
  fi

  log "Installed proxyctl binary from release asset: ${url}"
  return 0
}

install_proxyctl_from_source() {
  local target="$1"
  local tmpdir archive src_root output

  if ! command -v go >/dev/null 2>&1; then
    apt_install golang-go
  fi
  command -v go >/dev/null 2>&1 || return 1

  tmpdir="$(mktemp -d)"
  archive="${tmpdir}/proxyctl-source.tar.gz"
  output="${tmpdir}/proxyctl"

  if ! download_file "${PROXYCTL_SOURCE_ARCHIVE_URL}" "${archive}"; then
    rm -rf "${tmpdir}"
    return 1
  fi
  if ! tar -xzf "${archive}" -C "${tmpdir}"; then
    rm -rf "${tmpdir}"
    return 1
  fi

  src_root="$(find "${tmpdir}" -mindepth 1 -maxdepth 1 -type d | head -n1 || true)"
  if [[ -z "${src_root}" ]]; then
    rm -rf "${tmpdir}"
    return 1
  fi

  if ! (
    cd "${src_root}" && \
      GOCACHE="${tmpdir}/.cache/go-build" \
      GOMODCACHE="${tmpdir}/.cache/go-mod" \
      go build -trimpath -o "${output}" ./cmd/proxyctl
  ); then
    rm -rf "${tmpdir}"
    return 1
  fi

  install -m 0755 "${output}" "${target}"
  rm -rf "${tmpdir}"
  log "Installed proxyctl binary from source build (${PROXYCTL_SOURCE_ARCHIVE_URL})"
  return 0
}

verify_proxyctl_binary() {
  local target="$1"
  "${target}" --version >/dev/null 2>&1 || return 1
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
  local channel
  channel="$(printf '%s' "${PROXYCTL_INSTALL_CHANNEL}" | tr '[:upper:]' '[:lower:]')"

  case "${channel}" in
    auto|release|source|url|local)
      ;;
    *)
      fail "invalid PROXYCTL_INSTALL_CHANNEL=${PROXYCTL_INSTALL_CHANNEL}. Expected: auto|release|source|url|local"
      ;;
  esac

  if [[ -x "${target}" && "${PROXYCTL_REINSTALL_BINARY}" != "1" && -z "${PROXYCTL_BINARY_URL}" && "${channel}" == "auto" ]]; then
    log "Existing proxyctl binary found at ${target}; skipping reinstall"
    return 0
  fi

  if [[ -n "${PROXYCTL_BINARY_URL}" ]]; then
    if install_proxyctl_from_url "${PROXYCTL_BINARY_URL}" "${target}"; then
      log "Installed proxyctl binary from PROXYCTL_BINARY_URL"
      if ! verify_proxyctl_binary "${target}"; then
        fail "installed proxyctl binary is not executable"
      fi
      return 0
    fi

    if is_proxyctl_latest_download_url "${PROXYCTL_BINARY_URL}"; then
      warn "Failed to download ${PROXYCTL_BINARY_URL}; trying auto-resolved latest release asset"
      if install_proxyctl_from_release "${target}"; then
        if ! verify_proxyctl_binary "${target}"; then
          fail "installed proxyctl binary is not executable"
        fi
        return 0
      fi
    fi

    if [[ "${channel}" == "url" ]]; then
      fail "failed to download proxyctl binary from ${PROXYCTL_BINARY_URL}"
    fi

    warn "Failed to install proxyctl via PROXYCTL_BINARY_URL; continuing with channel=${channel}"
  fi

  if [[ "${channel}" == "release" || "${channel}" == "auto" ]]; then
    if install_proxyctl_from_release "${target}"; then
      if ! verify_proxyctl_binary "${target}"; then
        fail "installed proxyctl binary is not executable"
      fi
      return 0
    fi
    [[ "${channel}" == "auto" ]] || fail "failed to install proxyctl binary from release assets"
    warn "Release asset install failed; trying source build"
  fi

  if [[ "${channel}" == "source" || "${channel}" == "auto" ]]; then
    if install_proxyctl_from_source "${target}"; then
      if ! verify_proxyctl_binary "${target}"; then
        fail "installed proxyctl binary is not executable"
      fi
      return 0
    fi
    [[ "${channel}" == "auto" ]] || fail "failed to build/install proxyctl from source"
    warn "Source build failed; trying local binary and main branch URL fallback"
  fi

  if [[ "${channel}" == "local" || "${channel}" == "auto" ]]; then
    local local_binary="${SCRIPT_DIR}/proxyctl"
    if [[ -x "${local_binary}" ]]; then
      install -m 0755 "${local_binary}" "${target}"
      log "Installed proxyctl binary from ${local_binary}"
      if ! verify_proxyctl_binary "${target}"; then
        fail "installed proxyctl binary is not executable"
      fi
      return 0
    fi
    [[ "${channel}" == "auto" ]] || fail "local proxyctl binary not found at ${local_binary}"
  fi

  if [[ "${channel}" == "auto" ]]; then
    if install_proxyctl_from_url "${PROXYCTL_MAIN_BINARY_URL}" "${target}"; then
      log "Installed proxyctl binary from main branch URL fallback"
      if ! verify_proxyctl_binary "${target}"; then
        fail "installed proxyctl binary is not executable"
      fi
      return 0
    fi
    warn "Main branch binary URL fallback failed"
  fi

  if [[ -x "${target}" ]]; then
    log "Using existing proxyctl binary at ${target}"
    return 0
  fi

  fail "proxyctl binary source is not available. Use PROXYCTL_BINARY_URL or PROXYCTL_INSTALL_CHANNEL=source"
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
    if ! extract_and_install_binary "${tmp}" "${binary_name}" "${BIN_DIR}/${binary_name}"; then
      rm -rf "${tmpdir}"
      fail "failed to install ${binary_name} from ${env_url}"
    fi
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
    "${SHARE_ROOT}"
    "${SHARE_TEMPLATES_DIR}"
    "${SHARE_ACTIVE_DECOY_DIR}"
    "${SHARE_ACTIVE_DECOY_DIR}/assets"
    "${SHARE_DECOY_LIBRARY_DIR}"
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
  local singbox_content xray_content caddy_content nginx_content panel_content

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

  panel_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/systemd/proxyctl-panel.service" "$(cat <<'EOT'
[Unit]
Description=proxyctl web panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/proxyctl panel serve --config /etc/proxy-orchestrator/proxyctl.yaml
Restart=always
RestartSec=2s

[Install]
WantedBy=multi-user.target
EOT
)")"

  write_managed_file "${SYSTEMD_DIR}/proxyctl-sing-box.service" 0644 "${singbox_content}"
  write_managed_file "${SYSTEMD_DIR}/proxyctl-xray.service" 0644 "${xray_content}"
  write_managed_file "${SYSTEMD_DIR}/proxyctl-caddy.service" 0644 "${caddy_content}"
  write_managed_file "${SYSTEMD_DIR}/proxyctl-nginx.service" 0644 "${nginx_content}"
  write_managed_file "${SYSTEMD_DIR}/proxyctl-panel.service" 0644 "${panel_content}"

  systemctl daemon-reload
  log "Installed systemd units and reloaded daemon"
}

install_uninstall_script() {
  local script_content
  script_content="$(read_packaged_file_or_default "${SCRIPT_DIR}/packaging/scripts/proxyctl-uninstall.sh" "$(cat <<'EOT'
#!/usr/bin/env bash
set -Eeuo pipefail

IFS=$'\n\t'

TAG="proxyctl-uninstall"
YES=0
REMOVE_RUNTIME_PACKAGES=0

log() {
  printf '[%s] %s\n' "${TAG}" "$*"
}

fail() {
  log "ERROR: $*"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --yes) YES=1 ;;
    --remove-runtime-packages) REMOVE_RUNTIME_PACKAGES=1 ;;
    *) fail "unknown argument: $1" ;;
  esac
  shift
done

if [[ "${YES}" -ne 1 ]]; then
  printf "This will remove proxyctl and its data. Continue? [y/N]: "
  read -r ans
  ans="$(printf '%s' "${ans}" | tr '[:upper:]' '[:lower:]')"
  if [[ "${ans}" != "y" && "${ans}" != "yes" ]]; then
    log "Cancelled"
    exit 1
  fi
fi

for unit in \
  proxyctl-sing-box.service \
  proxyctl-xray.service \
  proxyctl-caddy.service \
  proxyctl-nginx.service \
  proxyctl-panel.service \
  proxyctl-self-update.service \
  proxyctl-self-update.timer \
  caddy.service \
  nginx.service \
  sing-box.service \
  singbox.service \
  xray.service \
  xray-core.service; do
  if systemctl list-unit-files "${unit}" >/dev/null 2>&1; then
    systemctl disable --now "${unit}" >/dev/null 2>&1 || true
  fi
done
rm -f /etc/systemd/system/proxyctl-*.service /etc/systemd/system/proxyctl-self-update.timer
systemctl daemon-reload || true
systemctl reset-failed || true

rm -f /usr/local/bin/proxyctl /usr/local/bin/sing-box /usr/local/bin/xray /usr/local/sbin/proxyctl-self-update /usr/local/sbin/proxyctl-uninstall
rm -rf /etc/proxy-orchestrator /var/lib/proxy-orchestrator /var/backups/proxy-orchestrator /usr/share/proxy-orchestrator /var/log/proxy-orchestrator

if [[ -d /root/.ssh ]]; then
  shopt -s nullglob
  for pub in /root/.ssh/*.pub; do
    if grep -q "proxyctl-auto-" "${pub}" 2>/dev/null; then
      rm -f "${pub%.pub}" "${pub}"
    fi
  done
  shopt -u nullglob
fi

rm -rf /caddy /var/lib/caddy /var/log/caddy /etc/ssl/caddy

if [[ "${REMOVE_RUNTIME_PACKAGES}" -eq 1 ]]; then
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get purge -y caddy nginx || true
    DEBIAN_FRONTEND=noninteractive apt-get autoremove -y || true
  fi
fi

log "proxyctl purge completed"
EOT
)")"

  write_managed_file "${SBIN_DIR}/proxyctl-uninstall" 0755 "${script_content}"
}

install_auto_update() {
  if [[ "${PROXYCTL_ENABLE_AUTO_UPDATE}" != "1" ]]; then
    return 0
  fi

  local updater_script="/usr/local/sbin/proxyctl-self-update"
  local updater_service="${SYSTEMD_DIR}/proxyctl-self-update.service"
  local updater_timer="${SYSTEMD_DIR}/proxyctl-self-update.timer"
  local script_content service_content timer_content

  script_content="$(cat <<EOT
#!/usr/bin/env bash
set -Eeuo pipefail
curl -fsSL '${PROXYCTL_AUTO_UPDATE_INSTALL_URL}' | PROXYCTL_REINSTALL_BINARY=1 PROXYCTL_INSTALL_CHANNEL='${PROXYCTL_INSTALL_CHANNEL}' PROXYCTL_ENABLE_AUTO_UPDATE=1 bash
EOT
)"

  service_content="$(cat <<'EOT'
[Unit]
Description=proxyctl self update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/proxyctl-self-update
EOT
)"

  timer_content="$(cat <<EOT
[Unit]
Description=Run proxyctl self update periodically

[Timer]
OnCalendar=${PROXYCTL_AUTO_UPDATE_SCHEDULE}
Persistent=true
RandomizedDelaySec=30m

[Install]
WantedBy=timers.target
EOT
)"

  write_managed_file "${updater_script}" 0755 "${script_content}"
  write_managed_file "${updater_service}" 0644 "${service_content}"
  write_managed_file "${updater_timer}" 0644 "${timer_content}"

  systemctl daemon-reload
  systemctl enable --now proxyctl-self-update.timer
  log "Enabled auto-update timer: proxyctl-self-update.timer (${PROXYCTL_AUTO_UPDATE_SCHEDULE})"
}

install_default_config() {
  if [[ -f "${CONFIG_PATH}" ]]; then
    log "Keeping existing file: ${CONFIG_PATH}"
    return 0
  fi

  local https_value="false"
  if [[ "${SELECTED_REVERSE_PROXY}" == "caddy" && -n "${SELECTED_PUBLIC_DOMAIN}" ]]; then
    https_value="true"
  fi

  cat >"${CONFIG_PATH}" <<EOT
deployment_mode: ${SELECTED_DEPLOYMENT_MODE}
reverse_proxy: ${SELECTED_REVERSE_PROXY}

storage:
  sqlite_path: /var/lib/proxy-orchestrator/proxyctl.db

runtime:
  singbox_unit: proxyctl-sing-box.service
  xray_unit: proxyctl-xray.service
  caddy_unit: proxyctl-caddy.service
  nginx_unit: proxyctl-nginx.service

public:
  domain: "${SELECTED_PUBLIC_DOMAIN}"
  https: ${https_value}
  contact_email: "${SELECTED_CONTACT_EMAIL}"
EOT
  chmod 0640 "${CONFIG_PATH}"
  log "Created: ${CONFIG_PATH}"
}

install_default_runtime_files() {
  local caddy_content nginx_content decoy_index decoy_style

  caddy_content="$(cat <<'EOT'
:80 {
  root * /etc/proxy-orchestrator/runtime/decoy-site
  file_server
}
EOT
)"
  if [[ "${SELECTED_REVERSE_PROXY}" == "caddy" && -n "${SELECTED_PUBLIC_DOMAIN}" ]]; then
    if [[ -n "${SELECTED_CONTACT_EMAIL}" ]]; then
      caddy_content="$(cat <<EOT
{
  email ${SELECTED_CONTACT_EMAIL}
}

${SELECTED_PUBLIC_DOMAIN} {
  root * /etc/proxy-orchestrator/runtime/decoy-site
  file_server
}
EOT
)"
    else
      caddy_content="$(cat <<EOT
${SELECTED_PUBLIC_DOMAIN} {
  root * /etc/proxy-orchestrator/runtime/decoy-site
  file_server
}
EOT
)"
    fi
  elif [[ -f "${SCRIPT_DIR}/packaging/defaults/runtime/caddy/Caddyfile" ]]; then
    caddy_content="$(cat "${SCRIPT_DIR}/packaging/defaults/runtime/caddy/Caddyfile")"
  fi

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
  build_decoy_assets "${SELECTED_DECOY_TEMPLATE}"
  decoy_index="${DECOY_INDEX_CONTENT}"
  decoy_style="${DECOY_STYLE_CONTENT}"

  if [[ "${SELECTED_REVERSE_PROXY}" == "caddy" && -n "${SELECTED_PUBLIC_DOMAIN}" ]]; then
    if [[ -f "${CADDY_RUNTIME_DIR}/Caddyfile" ]]; then
      if grep -Eq '^[[:space:]]*:80[[:space:]]*\{' "${CADDY_RUNTIME_DIR}/Caddyfile"; then
        write_managed_file "${CADDY_RUNTIME_DIR}/Caddyfile" 0640 "${caddy_content}"
      else
        log "Keeping existing custom file: ${CADDY_RUNTIME_DIR}/Caddyfile"
      fi
    else
      write_if_absent "${CADDY_RUNTIME_DIR}/Caddyfile" 0640 "${caddy_content}"
    fi
  else
    write_if_absent "${CADDY_RUNTIME_DIR}/Caddyfile" 0640 "${caddy_content}"
  fi

  write_if_absent "${NGINX_RUNTIME_DIR}/nginx.conf" 0640 "${nginx_content}"
  activate_decoy_template "${decoy_index}" "${decoy_style}"
}

ensure_caddy_panel_route() {
  if [[ "${SELECTED_REVERSE_PROXY}" != "caddy" ]]; then
    return 0
  fi
  if [[ "${SELECTED_DEPLOYMENT_MODE}" == "node" ]]; then
    return 0
  fi
  if [[ -z "${SELECTED_PUBLIC_DOMAIN}" || -z "${SELECTED_PANEL_PATH}" || -z "${SELECTED_PANEL_PORT}" ]]; then
    return 0
  fi

  local caddy_file="${CADDY_RUNTIME_DIR}/Caddyfile"
  if [[ ! -f "${caddy_file}" ]]; then
    warn "Caddyfile is missing, cannot ensure panel route: ${caddy_file}"
    return 0
  fi

  local route_marker="handle ${SELECTED_PANEL_PATH}* {"
  if grep -Fq "${route_marker}" "${caddy_file}"; then
    return 0
  fi

  local rendered tmp
  tmp="$(mktemp)"
  local awk_rc=0
  awk \
    -v domain="${SELECTED_PUBLIC_DOMAIN}" \
    -v panel_path="${SELECTED_PANEL_PATH}" \
    -v panel_port="${SELECTED_PANEL_PORT}" \
    '
      function trim(s) {
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
        return s
      }
      BEGIN {
        inserted = 0
      }
      {
        line = $0
        t = trim(line)
        print line
        if (inserted == 0) {
          if (index(t, domain) == 1) {
            rest = substr(t, length(domain) + 1)
            rest = trim(rest)
            if (rest == "{") {
              print "  handle " panel_path "* {"
              print "    reverse_proxy 127.0.0.1:" panel_port
              print "  }"
              inserted = 1
            }
          }
        }
      }
      END {
        if (inserted == 0) {
          exit 42
        }
      }
    ' "${caddy_file}" >"${tmp}" || awk_rc=$?
  if [[ "${awk_rc}" -ne 0 ]]; then
    rm -f "${tmp}"
    if [[ "${awk_rc}" -eq 42 ]]; then
      warn "Could not find '${SELECTED_PUBLIC_DOMAIN} {' block in ${caddy_file}; skipping automatic panel route insertion"
      return 0
    fi
    warn "Failed to patch Caddyfile with panel route"
    return 0
  fi

  rendered="$(cat "${tmp}")"
  rm -f "${tmp}"
  write_managed_file "${caddy_file}" 0640 "${rendered}"
  log "Ensured panel route in ${caddy_file}: ${SELECTED_PANEL_PATH} -> 127.0.0.1:${SELECTED_PANEL_PORT}"
}

ensure_selected_reverse_proxy_service() {
  disable_stock_reverse_proxy_services

  if [[ "${SELECTED_REVERSE_PROXY}" != "caddy" && "${SELECTED_REVERSE_PROXY}" != "nginx" ]]; then
    warn "Unknown reverse proxy selection: ${SELECTED_REVERSE_PROXY}; skipping service management"
    return 0
  fi

  if [[ "${SELECTED_REVERSE_PROXY}" == "caddy" && "${PROXYCTL_ENABLE_CADDY_ON_INSTALL}" != "1" ]]; then
    log "Skipping caddy auto-enable (PROXYCTL_ENABLE_CADDY_ON_INSTALL=${PROXYCTL_ENABLE_CADDY_ON_INSTALL})"
    return 0
  fi

  local selected_unit=""
  local other_unit=""
  if [[ "${SELECTED_REVERSE_PROXY}" == "caddy" ]]; then
    selected_unit="proxyctl-caddy.service"
    other_unit="proxyctl-nginx.service"
  else
    selected_unit="proxyctl-nginx.service"
    other_unit="proxyctl-caddy.service"
  fi

  if systemctl list-unit-files "${other_unit}" >/dev/null 2>&1; then
    systemctl disable --now "${other_unit}" >/dev/null 2>&1 || true
  fi

  if ! systemctl list-unit-files "${selected_unit}" >/dev/null 2>&1; then
    warn "${selected_unit} is not installed; skipping reverse proxy auto-enable"
    return 0
  fi

  log "Ensuring ${selected_unit} is enabled and running"
  if systemctl enable --now "${selected_unit}" >/dev/null 2>&1; then
    log "${selected_unit} is enabled and active"
  else
    warn "Failed to enable/start ${selected_unit}. Check: systemctl status ${selected_unit}"
  fi
}

disable_stock_reverse_proxy_services() {
  local stock_unit
  for stock_unit in caddy.service nginx.service; do
    if systemctl list-unit-files "${stock_unit}" >/dev/null 2>&1; then
      log "Disabling stock reverse proxy unit: ${stock_unit}"
      systemctl disable --now "${stock_unit}" >/dev/null 2>&1 || true
    fi
  done
}

ensure_panel_service() {
  local panel_unit="proxyctl-panel.service"

  if ! systemctl list-unit-files "${panel_unit}" >/dev/null 2>&1; then
    warn "${panel_unit} is not installed; skipping panel service management"
    return 0
  fi

  if [[ "${SELECTED_DEPLOYMENT_MODE}" == "node" ]]; then
    log "Deployment mode is node; disabling ${panel_unit}"
    systemctl disable --now "${panel_unit}" >/dev/null 2>&1 || true
    return 0
  fi

  log "Ensuring ${panel_unit} is enabled and running"
  if systemctl enable --now "${panel_unit}" >/dev/null 2>&1; then
    log "${panel_unit} is enabled and active"
  else
    warn "Failed to enable/start ${panel_unit}. Check: systemctl status ${panel_unit}"
  fi
}

init_sqlite() {
  [[ -x "${BIN_DIR}/proxyctl" ]] || fail "proxyctl binary is not installed"

  if "${BIN_DIR}/proxyctl" init --config "${CONFIG_PATH}" >/dev/null 2>&1; then
    :
  elif "${BIN_DIR}/proxyctl" init --db "${DB_PATH}" >/dev/null 2>&1; then
    :
  elif "${BIN_DIR}/proxyctl" init >/dev/null 2>&1; then
    :
  else
    warn "proxyctl init failed or is not implemented in this binary; continuing without schema bootstrap"
    return 0
  fi

  chmod 0640 "${DB_PATH}" || true
  log "Initialized SQLite database: ${DB_PATH}"
}

print_next_steps() {
  cat <<'EOF_STEPS'

Installation finished.

Next steps:
1. Review configuration:
   editor /etc/proxy-orchestrator/proxyctl.yaml
2. Bootstrap data model:
   proxyctl node add --name primary --host <server-domain-or-ip>
   proxyctl user add --name <username>
3. Add inbounds and render runtime files:
   proxyctl inbound add --type vless --transport ws --node-id <node-id> --port 8443 --path /ws
   proxyctl render --config /etc/proxy-orchestrator/proxyctl.yaml
4. Start only required services:
   systemctl enable --now proxyctl-sing-box.service
   # proxyctl-caddy.service is auto-enabled on install by default
   # proxyctl-panel.service is auto-enabled in panel/panel+node modes
   systemctl status proxyctl-caddy.service --no-pager
   systemctl status proxyctl-panel.service --no-pager
5. Apply validated runtime update flow:
   proxyctl validate --config /etc/proxy-orchestrator/proxyctl.yaml
   proxyctl apply --config /etc/proxy-orchestrator/proxyctl.yaml
6. Optional auto-update timer:
   PROXYCTL_ENABLE_AUTO_UPDATE=1 bash install.sh
   systemctl list-timers proxyctl-self-update.timer
7. Decoy template library and switching:
   # Upload your own templates here: <name>/index.html + <name>/assets/style.css
   ls -la /usr/share/proxy-orchestrator/decoy-templates
   proxyctl wizard   # Settings -> switch decoy template
8. Full uninstall (purge):
   proxyctl uninstall --yes
EOF_STEPS

  if [[ "${SELECTED_DEPLOYMENT_MODE}" != "node" ]]; then
    cat <<EOF_PANEL

Panel access credentials (stored in ${PANEL_CREDENTIALS_PATH}):
- URL path: ${SELECTED_PANEL_PATH}
- Port: ${SELECTED_PANEL_PORT}
- Login: ${SELECTED_PANEL_LOGIN}
- Password: ${SELECTED_PANEL_PASSWORD}
- URL hint: ${PANEL_URL_HINT}
- Read again later: cat ${PANEL_CREDENTIALS_PATH}
- Save backup copy:
  cp ${PANEL_CREDENTIALS_PATH} ~/proxyctl-panel-credentials-\$(date -u +%Y%m%dT%H%M%SZ).env

You can override these on reinstall via:
  PROXYCTL_PANEL_PATH=/my-secret-path
  PROXYCTL_PANEL_PORT=28443
  PROXYCTL_PANEL_LOGIN=admin
  PROXYCTL_PANEL_PASSWORD='<strong-password>'
EOF_PANEL
  fi
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

  configure_install_preferences
  prepare_panel_credentials
  ensure_directories
  install_systemd_units
  install_uninstall_script
  install_auto_update
  install_decoy_template_library
  install_default_config
  install_default_runtime_files
  write_panel_credentials
  ensure_caddy_panel_route
  ensure_selected_reverse_proxy_service
  ensure_panel_service
  init_sqlite

  log "Installed ${APP_NAME} ${PROXYCTL_VERSION} layout on host"
  print_next_steps
}

main "$@"
