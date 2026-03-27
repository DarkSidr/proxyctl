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
    proxyctl-panel.service
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

remove_remote_proxyctl_ssh_keys() {
  local db_path="/var/lib/proxy-orchestrator/proxyctl.db"
  if [[ ! -f "${db_path}" ]]; then
    return 0
  fi
  if ! command -v sqlite3 >/dev/null 2>&1; then
    log "sqlite3 is unavailable; skipping remote SSH key cleanup"
    return 0
  fi
  if ! command -v ssh >/dev/null 2>&1; then
    log "ssh client is unavailable; skipping remote SSH key cleanup"
    return 0
  fi

  local hosts=()
  mapfile -t hosts < <(sqlite3 "${db_path}" "SELECT DISTINCT TRIM(host) FROM nodes WHERE TRIM(host) <> '';" 2>/dev/null || true)
  if [[ "${#hosts[@]}" -eq 0 ]]; then
    return 0
  fi

  local host
  for host in "${hosts[@]}"; do
    host="$(printf '%s' "${host}" | xargs || true)"
    if [[ -z "${host}" ]]; then
      continue
    fi
    log "Removing remote proxyctl SSH keys on host: ${host}"
    if ssh -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -- "root@${host}" \
      "if [ -f ~/.ssh/authorized_keys ]; then tmp=\$(mktemp); grep -v 'proxyctl-auto-' ~/.ssh/authorized_keys > \"\$tmp\"; cat \"\$tmp\" > ~/.ssh/authorized_keys; rm -f \"\$tmp\"; chmod 600 ~/.ssh/authorized_keys; fi" \
      >/dev/null 2>&1; then
      log "Remote key cleanup completed: ${host}"
    else
      log "WARNING: remote key cleanup failed for ${host} (password/sudo/manual access may be required)"
    fi
  done
}

final_sweep_and_report() {
  local cleanup_targets=(
    /etc/proxy-orchestrator
    /var/lib/proxy-orchestrator
    /var/backups/proxy-orchestrator
    /usr/share/proxy-orchestrator
    /var/log/proxy-orchestrator
  )

  rm -f /var/lib/proxy-orchestrator/proxyctl.db /var/lib/proxy-orchestrator/proxyctl.db-* 2>/dev/null || true
  rm -f /var/lib/proxy-orchestrator/*.db /var/lib/proxy-orchestrator/*.db-* 2>/dev/null || true
  rm -rf "${cleanup_targets[@]}" || true

  local leftover=0 target
  for target in "${cleanup_targets[@]}"; do
    if [[ -e "${target}" ]]; then
      leftover=1
      log "WARNING: residual path remains: ${target}"
      ls -la "${target}" 2>/dev/null || true
    fi
  done

  if [[ "${leftover}" -eq 0 ]]; then
    log "Post-clean verification: no proxyctl data paths remain"
  fi
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
  remove_remote_proxyctl_ssh_keys
  remove_files
  remove_proxyctl_ssh_keys
  remove_proxyctl_certificates_and_cache
  remove_runtime_packages
  final_sweep_and_report

  log "proxyctl purge completed"
}

main "$@"
