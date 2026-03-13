# Stage 12 Report: status / logs / doctor

## Что реализовано
- Добавлены эксплуатационные команды CLI:
  - `proxyctl status`
  - `proxyctl logs`
  - `proxyctl doctor`
- Команды реализованы в `internal/cli/diagnostics.go` и встроены в root CLI.

## `proxyctl status`
Показывает:
- состояние SQLite:
  - путь БД;
  - наличие файла;
  - признак инициализации схемы (по обязательным таблицам);
- runtime files по текущему layout:
  - `sing-box.json`;
  - `xray.json`;
  - конфиг выбранного reverse proxy (`Caddyfile` или `nginx.conf`);
- systemd unit status:
  - `proxyctl-sing-box.service`;
  - `proxyctl-xray.service`;
  - `proxyctl-caddy.service`;
  - `proxyctl-nginx.service`;
- выбранный reverse proxy;
- counters из БД (`users`, `nodes`, `inbounds`).

## `proxyctl logs`
- Реализован вывод `journalctl` для relevant services с флагами:
  - `--lines/-n`
  - `--follow/-f`
  - `--since`
  - `--until`
- Поддержаны scope:
  - `runtime` (по умолчанию): `sing-box` + `xray` + активный reverse proxy unit;
  - `apply`: `proxyctl-apply.service` (опциональный operational unit).

## `proxyctl doctor`
Проверяет типовые проблемы:
- отсутствует runtime file;
- отсутствует reverse proxy config;
- нет пользователей;
- битые/неконсистентные пути;
- отсутствует/неинициализирована SQLite DB.

Поведение:
- выводит findings в формате `[LEVEL] check: message`;
- возвращает non-zero exit code при blocking issues (`ERROR`).

## Документация
- Обновлён `README.md`:
  - добавлен раздел `Troubleshooting` с operational командами и типовыми действиями по findings.
- Обновлён `ARCHITECTURE.md`:
  - в `CLI Surface` добавлен `proxyctl doctor`;
  - добавлена секция `12.1 Operational diagnostics scope (stage 12)`.
- Обновлён `docs/ROADMAP.md`:
  - добавлен этап 12 с границами и результатом.

## Обнаруженные расхождения и выбранная реализация
- Расхождение: в `docs/ROADMAP.md` до начала работ отсутствовал этап 12, хотя текущая задача уже задана как отдельный этап.
- Выбрана минимально отклоняющаяся реализация:
  - добавлен отдельный этап 12 в roadmap без изменения предыдущих этапов и без изменения архитектурных ADR.

## Проверки
- `gofmt -w internal/cli/diagnostics.go internal/cli/root.go internal/cli/commands.go` — успешно.
- `GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test ./...` — успешно.

## Что не делали (по ограничениям этапа)
- Не добавляли новые protocol features.
- Не добавляли multi-node.
- Не меняли архитектуру runtime/systemd.
