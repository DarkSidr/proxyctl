# Stage 11 Report: one-command installer

## Что реализовано
- Добавлен `install.sh` для MVP host-based bootstrap:
  - fail-fast проверка root/OS (`Debian 12`, `Ubuntu 22.04`, `Ubuntu 24.04`);
  - установка зависимостей и reverse-proxy runtime packages через `apt`;
  - установка `proxyctl` binary (из `PROXYCTL_BINARY_URL`, локального `./proxyctl` или существующего install);
  - проверка/установка `sing-box` и `xray` (через apt или fallback `SINGBOX_BINARY_URL`/`XRAY_BINARY_URL`);
  - создание архитектурного layout (`/etc`, `/var/lib`, `/var/backups`);
  - раскладка systemd unit-файлов и `systemctl daemon-reload`;
  - инициализация SQLite (`proxyctl init --db /var/lib/proxy-orchestrator/proxyctl.db`);
  - bootstrap default config, reverse proxy runtime и decoy site;
  - вывод next steps для оператора.

## Packaging
- Обновлены `packaging/systemd/*.service` под зафиксированный layout:
  - `proxyctl-sing-box.service`
  - `proxyctl-xray.service`
  - `proxyctl-caddy.service`
  - `proxyctl-nginx.service`
- Удалён legacy placeholder `packaging/systemd/proxyctl.service`.
- Добавлены defaults для installer-а:
  - `packaging/defaults/proxyctl.yaml`
  - `packaging/defaults/runtime/caddy/Caddyfile`
  - `packaging/defaults/runtime/nginx/nginx.conf`
  - `packaging/defaults/runtime/decoy-site/index.html`
  - `packaging/defaults/runtime/decoy-site/assets/style.css`

## Идемпотентность
- `proxyctl.yaml` не перезаписывается при повторном запуске.
- Runtime defaults создаются только при отсутствии.
- Managed unit-файлы обновляются только при изменении контента; перед обновлением создаётся timestamped backup.
- SQLite init выполняется безопасно на повторных запусках.

## Документация
- Обновлён `ARCHITECTURE.md`:
  - добавлены явные секции `6.0 Installation Layout` и `6.4 Runtime prerequisites`.
- Обновлён `docs/ROADMAP.md`:
  - добавлен этап 11 и его ограничения.
- Добавлены эксплуатационные заметки:
  - `docs/INSTALLER.md` (install/update/uninstall).
- `README.md` дополнен разделом installer entrypoint.

## Обнаруженные расхождения и выбранная реализация
- Обнаружено расхождение: в постановке этапа используются секции `Installation Layout` и `Runtime prerequisites`, но в `ARCHITECTURE.md` до этапа 11 эти секции не были выделены явно.
- Выбрана минимально отклоняющаяся реализация:
  - добавлены только недостающие архитектурные уточнения без изменения ранее принятых ADR;
  - installer реализован строго в рамках уже зафиксированного host-based layout (`/etc|/var/*/proxy-orchestrator`).

## Проверки
- `bash -n install.sh` — успешно.
- `go test ./...` — успешно.

## Что не делали (по требованиям)
- Не добавляли multi-node provisioning.
- Не добавляли Telegram integration.
- Не добавляли web UI.
