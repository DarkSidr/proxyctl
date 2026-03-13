# proxyctl Roadmap

## Этап 0 — Foundation
- Инициализировать структуру проекта и базовый CLI каркас.
- Определить domain-модели и контракты модулей.
- Подготовить файловую структуру runtime/state/revisions.

Результат этапа:
- Команда `proxyctl` с базовыми группами команд.
- Согласованные интерфейсы между слоями.

## Этап 1 — Persistence и ревизии
- Внедрить SQLite схему для:
  - сущностей конфигурации;
  - `ConfigRevision`;
  - `ApplyOperation`.
- Добавить механизмы записи/чтения ревизий.

Результат этапа:
- Транзакционное хранение состояния.
- История ревизий и операций.

Примечание по текущей поставке:
- Для CLI skeleton сначала реализуется подэтап `1a/2` — базовые domain-сущности (`User`, `Node`, `Inbound`, `Credential`, `Subscription`) и SQLite storage для них (`init`, `user/node list/add`, `inbound list`), без `render/validate/apply`.

## Этап 2 — Config Builder (MVP протоколы)
- Реализовать генерацию конфигураций:
  - VLESS -> sing-box;
  - Hysteria2 -> sing-box;
  - XHTTP -> Xray.
- Реализовать builder для reverse proxy:
  - Caddy default;
  - Nginx optional.
- Добавить wiring decoy site.

Результат этапа:
- Полный генератор артефактов ревизии в staging.

## Этап 3 — Validation Engine
- Валидация входных параметров и domain-ограничений.
- Предприменительная проверка артефактов и конфликтов портов.
- Dry-run режим перед apply.

Результат этапа:
- Предсказуемый fail-fast до изменений в runtime.

Примечание по текущей поставке:
- До полного Validation Engine реализуется подэтап `3a` — protocol compatibility matrix и engine resolver для `inbound add` (без render/apply/runtime).

## Этап 4 — Apply Orchestrator (безопасный rollout)
- Реализовать pipeline:
  - `collect -> build -> validate -> persist -> switch -> restart/reload -> health-check`.
- Добавить атомарное переключение `active`.
- Добавить rollback на последнюю рабочую ревизию.

Результат этапа:
- Безопасное применение конфигураций с возможностью отката.

Примечание по текущей поставке:
- Перед полным apply выделяется подэтап `4a` — sing-box renderer (чистый config builder для `vless`/`hysteria2`, JSON preview, client artifacts, validator contract).
- Это не включает runtime apply/restart и не сдвигает обязательства этапа 4 по оркестрации rollout.

## Этап 5 — Runtime интеграция с systemd
- Управление `proxyctl-sing-box.service`, `proxyctl-xray.service`, `proxyctl-caddy.service`, `proxyctl-nginx.service`.
- Реализовать корректный порядок restart/reload по затронутым компонентам.

Результат этапа:
- Контролируемый жизненный цикл сервисов через CLI.

Примечание по текущей поставке:
- Перед полным runtime integration выделяется подэтап `5a` — Xray renderer (чистый config builder для `xhttp`, JSON preview, client artifacts, validator contract).
- Это не включает runtime/systemd/restart и не сдвигает обязательства этапа 5 по интеграции с systemd.

## Этап 6 — Hardening MVP
- Логирование операций apply/rollback.
- Smoke-check после применения.
- Обработка типовых сбоев (failed restart, partial apply).

Результат этапа:
- Эксплуатационно устойчивый single-node MVP.

Примечание по текущей поставке:
- Перед полным hardening выделяется подэтап `6a` — subscription service (агрегация client artifacts из sing-box/xray, локальные форматы `txt/base64/json`, сохранение в data dir, JSON export как контракт для будущей Telegram automation).
- Это не включает Telegram bot, HTTP delivery endpoint и remote nodes.

## Этап 7 — Подготовка к расширениям
- Зафиксировать extension points для multi-node.
- Зафиксировать adapter-границы для Telegram bot integration.
- Подготовить backward-compatible контракты команд.

Результат этапа:
- MVP готов к эволюции без ломки ядра.

Примечание по текущей поставке:
- Перед extension-oriented задачами выделяется подэтап `7a` — runtime layout и file generation:
  - фиксированные host-директории `/etc|/var/*/proxy-orchestrator`;
  - безопасная запись runtime файлов (atomic write + backups);
  - команды `preview/render` без `apply/restart`.
- Это не включает `systemctl restart`, `install.sh` и provisioning `caddy/nginx`.

## Этап 8 — Runtime apply pipeline (validate/apply/restart/rollback)
- Реализовать безопасный pipeline применения runtime-конфигов:
  - `collect -> build -> backup -> validate -> apply -> restart/reload -> rollback`.
- Добавить CLI-команды:
  - `proxyctl validate`;
  - `proxyctl apply [--dry-run]`.
- Ввести абстракции:
  - `ProcessValidator`;
  - `ServiceManager`.

Результат этапа:
- Runtime-конфиги не перезаписываются вслепую; rollback-path обязателен при failed restart.

Примечание по текущей поставке:
- Это подэтап `8a` относительно полного целевого pipeline из архитектуры (`persist/switch/health-check` сохраняются как следующий шаг и не отменяются).
- Не включает installer/provisioning (`install.sh`, reverse proxy provisioning, multi-node deploy).

## Этап 9 — Reverse proxy layer (Caddy default) и decoy site
- Реализовать отдельный reverse proxy слой для Caddy (без смешивания с protocol renderers).
- Добавить шаблонную генерацию `Caddyfile` из app-config + enabled inbounds.
- Добавить decoy-site templates и runtime раскладку статических assets.
- Добавить preview для Caddy config.

Результат этапа:
- `Caddyfile` и `Caddyfile.preview` генерируются в runtime layout.
- decoy site раскладывается в runtime dir и используется как default fallback.

Ограничения этапа:
- Nginx не реализуется в этом этапе.
- installer/provisioning и restart/reload caddy unit остаются вне scope.

## Этап 10 — Nginx как опция
- Добавить альтернативный backend `Nginx` в reverse proxy layer параллельно с Caddy.
- Добавить шаблонную генерацию `nginx.conf` и preview-режим для Nginx.
- Сохранить единый decoy-site runtime для Caddy/Nginx.
- Добавить выбор backend через `AppConfig.ReverseProxy` (`reverse_proxy: caddy|nginx`).

Результат этапа:
- Caddy остаётся default backend.
- Nginx доступен как optional backend без форков.
- `render/preview` выбирают backend по app-config и не ломают Caddy flow.

Ограничения этапа:
- Не включает provisioning/restart `proxyctl-nginx.service`.
- Не включает nginx forks и расширенные vendor-specific режимы.

## Этап 11 — Installer одной командой
- Реализовать `install.sh` для host-based установки (без Docker).
- Добавить проверку поддерживаемых ОС:
  - Debian 12;
  - Ubuntu 22.04;
  - Ubuntu 24.04.
- Добавить idempotent bootstrap:
  - установка бинарей и зависимостей;
  - подготовка layout `/etc|/var/*/proxy-orchestrator`;
  - раскладка systemd unit-файлов;
  - первичная инициализация SQLite;
  - default config + reverse proxy runtime + decoy site.
- Добавить uninstall/update notes в документацию.

Результат этапа:
- Установка `proxyctl` выполняется одной командой через `bash <(curl ...)`.
- Повторный запуск installer-а не ломает существующий config без явного действия пользователя.

Ограничения этапа:
- Не включает multi-node provisioning.
- Не включает Telegram integration.
- Не включает web UI.

## Этап 12 — Operational diagnostics (`status` / `logs` / `doctor`)
- Реализовать эксплуатационные команды CLI:
  - `proxyctl status`;
  - `proxyctl logs`;
  - `proxyctl doctor`.
- Подключить диагностику к зафиксированным runtime layout и systemd unit model.
- Добавить troubleshooting guidance в README.

Результат этапа:
- CLI пригоден для базовой диагностики прод-узла single-node.
- Оператор видит состояние DB/runtime/systemd и типовые проблемы без ручного разбора layout.

Ограничения этапа:
- Не добавляет новые protocol features.
- Не добавляет multi-node behavior.
- Не меняет принятые архитектурные решения по runtime/systemd.

## Этап 14 — Финальная инженерная полировка и testing checklist
- Провести связку документации и фактического CLI.
- Добавить ручной smoke checklist для MVP-flow.
- Добавить единые локальные check-таргеты (`build/test/vet/fmt-check`).
- Убрать слабые маркеры незавершённости (`TODO` в user-facing текстах), не меняя scope MVP.

Результат этапа:
- Репозиторий выглядит как целостный single-node MVP.
- Основной сценарий эксплуатации проверяем вручную по одному документу.

Ограничения этапа:
- Без добавления новых продуктовых фич.
- Без архитектурного рефакторинга.
- Без выхода за MVP-границы, зафиксированные в `ARCHITECTURE.md` и `docs/DECISIONS.md`.

## Порядок реализации (кратко)
1. Foundation
2. Persistence
3. Config Builder
4. Validation
5. Apply Orchestrator
6. systemd Runtime
7. Hardening
8. Extension points (multi-node + Telegram)
9. Runtime apply pipeline
10. Reverse proxy Caddy layer + decoy
11. Nginx optional backend
12. Installer/bootstrap flow
13. Operational diagnostics
14. Final engineering polish + testing checklist
