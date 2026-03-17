#!/usr/bin/env bash
set -Eeuo pipefail

IFS=$'\n\t'

TAG="proxyctl-uninstall"
REMOVE_RUNTIME_PACKAGES=0
YES=0

log() {
  printf '[%s] %s\n' "${TAG}" "$*"
}

fail() {
  log "ERROR: $*"
  exit 1
}

usage() {
  cat <<'EOF'
Usage: proxyctl-uninstall [--yes] [--remove-runtime-packages]

Removes proxyctl-managed services, binaries, runtime/config/state directories and helper scripts.
Also purges proxyctl-generated SSH keys and common proxyctl-related certificate/cache paths.

Options:
  --yes                     Skip interactive confirmation.
  --remove-runtime-packages Purge apt packages caddy/nginx after proxyctl cleanup.
EOF
}

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    fail "run as root"
  fi
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --yes)
        YES=1
        ;;
      --remove-runtime-packages)
        REMOVE_RUNTIME_PACKAGES=1
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        fail "unknown argument: $1"
        ;;
    esac
    shift
  done
}

confirm_if_needed() {
  if [[ "${YES}" -eq 1 ]]; then
    return 0
  fi
  printf "This will permanently remove proxyctl and its data from this VPS. Continue? [y/N]: "
  read -r ans
  ans="$(printf '%s' "${ans}" | tr '[:upper:]' '[:lower:]')"
  if [[ "${ans}" != "y" && "${ans}" != "yes" ]]; then
    log "Cancelled"
    exit 1
  fi
}

disable_units() {
  local proxyctl_units=(
    proxyctl-sing-box.service
    proxyctl-xray.service
    proxyctl-caddy.service
    proxyctl-nginx.service
    proxyctl-self-update.service
    proxyctl-self-update.timer
  )
  local runtime_units=(
    caddy.service
    nginx.service
    sing-box.service
    singbox.service
    xray.service
    xray-core.service
  )

  local unit
  for unit in "${proxyctl_units[@]}"; do
    if systemctl list-unit-files "${unit}" >/dev/null 2>&1; then
      systemctl disable --now "${unit}" >/dev/null 2>&1 || true
    fi
  done
  for unit in "${runtime_units[@]}"; do
    if systemctl list-unit-files "${unit}" >/dev/null 2>&1; then
      systemctl disable --now "${unit}" >/dev/null 2>&1 || true
    fi
  done
}

remove_units() {
  rm -f \
    /etc/systemd/system/proxyctl-sing-box.service \
    /etc/systemd/system/proxyctl-xray.service \
    /etc/systemd/system/proxyctl-caddy.service \
    /etc/systemd/system/proxyctl-nginx.service \
    /etc/systemd/system/proxyctl-self-update.service \
    /etc/systemd/system/proxyctl-self-update.timer
  systemctl daemon-reload || true
  systemctl reset-failed || true
}

remove_files() {
  rm -f \
    /usr/local/bin/proxyctl \
    /usr/local/bin/sing-box \
    /usr/local/bin/xray \
    /usr/local/sbin/proxyctl-self-update \
    /usr/local/sbin/proxyctl-uninstall

  rm -rf \
    /etc/proxy-orchestrator \
    /var/lib/proxy-orchestrator \
    /var/backups/proxy-orchestrator \
    /usr/share/proxy-orchestrator \
    /var/log/proxy-orchestrator
}

remove_proxyctl_ssh_keys() {
  local ssh_dir="/root/.ssh"
  if [[ ! -d "${ssh_dir}" ]]; then
    return 0
  fi

  local pub key base
  shopt -s nullglob
  for pub in "${ssh_dir}"/*.pub; do
    base="${pub%.pub}"
    if grep -q "proxyctl-auto-" "${pub}" 2>/dev/null; then
      log "Removing proxyctl-generated SSH key: ${base}"
      rm -f "${base}" "${pub}"
    fi
  done
  shopt -u nullglob
}

remove_proxyctl_certificates_and_cache() {
  rm -rf \
    /caddy \
    /var/lib/caddy \
    /var/log/caddy \
    /etc/ssl/caddy
}

remove_runtime_packages() {
  if [[ "${REMOVE_RUNTIME_PACKAGES}" -ne 1 ]]; then
    return 0
  fi
  if ! command -v apt-get >/dev/null 2>&1; then
    log "apt-get is unavailable; skipping runtime package purge"
    return 0
  fi
  log "Purging runtime packages: caddy nginx"
  DEBIAN_FRONTEND=noninteractive apt-get purge -y caddy nginx || true
  DEBIAN_FRONTEND=noninteractive apt-get autoremove -y || true
}

main() {
  require_root
  parse_args "$@"
  confirm_if_needed

  disable_units
  remove_units
  remove_files
  remove_proxyctl_ssh_keys
  remove_proxyctl_certificates_and_cache
  remove_runtime_packages

  log "proxyctl purge completed"
}

main "$@"
