# proxyctl — Architecture (MVP)

## 1. Цели проекта
- Дать self-hosted CLI-инструмент для управления VPN/прокси-стеком на VPS без web UI.
- Унифицировать работу с несколькими протоколами через единый UX (`proxyctl`).
- Обеспечить безопасный жизненный цикл конфигураций: `validate -> apply -> restart/rollback`.
- Сохранить архитектурную совместимость с будущим multi-node и интеграцией с Telegram bot.

## 2. Границы MVP

### Входит в MVP
- Один сервер (single-node).
- Протоколы:
  - VLESS через sing-box.
  - Hysteria2 через sing-box.
  - XHTTP через Xray.
- Reverse proxy:
  - Caddy по умолчанию.
  - Nginx как опциональный backend.
- Установка одной командой через `install.sh` (вне этого документа).
- Decoy site для маскировки сервиса.
- Безопасный apply pipeline с валидацией и атомарным переключением активной конфигурации.

### Не входит в MVP
- Web UI.
- Кластерный контроль и оркестрация нескольких узлов.
- Полноценный Telegram bot (только точки расширения).
- Автоматический autoscaling и service discovery.

## 3. Модули системы
1. CLI Layer
- Парсинг команд и флагов.
- Вызов use-case сценариев (создание, обновление, проверка, применение).

2. Domain Layer
- Модели: узел, инстанс протокола, reverse proxy, сертификаты, decoy site.
- Правила совместимости и ограничения MVP.

3. Config Builder
- Генерация runtime-конфигураций sing-box/Xray/reverse proxy на основании domain-сущностей.
- Детерминированный output (одинаковый input -> одинаковый output).

4. Validation Engine
- Синтаксическая и семантическая проверка входных данных.
- Предприменительная проверка с dry-run подходом.

5. Apply Orchestrator
- Подготовка staging-артефактов.
- Атомарный switch active config.
- Координация restart/reload сервисов.
- Rollback при ошибке.

6. Persistence
- SQLite (MVP) для хранения состояния, версий конфигурации и аудита операций.

7. Runtime Control
- Интеграция с systemd (start/stop/restart/status).
- Проверка health после применения.

8. Installer Interface
- Точки входа для install.sh (bootstrap зависимостей и директорий).

## 4. Внутренние сущности данных

Для CLI/domain/storage MVP фиксируется базовый control-plane набор:

1. User
- `id`
- `name`
- `enabled`
- `created_at`

2. Node
- `id`
- `name`
- `host`
- `role` (MVP: `primary`)
- `enabled`
- `created_at`

3. Inbound
- `id`
- `type` (`vless`, `hysteria2`, `xhttp`)
- `engine` (`sing-box`, `xray`)
- `node_id`
- `domain`
- `port`
- `tls_enabled`
- `transport`
- `path`
- `sni`
- `enabled`
- `created_at`

4. Credential
- `id`
- `user_id`
- `inbound_id`
- `kind`
- `secret`
- `metadata` (JSON/string)
- `created_at`

5. Subscription
- `id`
- `user_id`
- `format`
- `output_path`
- `updated_at`

Сущности ниже остаются в архитектуре для следующих этапов (`render/validate/apply`):

6. ConfigRevision
- `id`
- `revision`
- `status` (`draft`, `validated`, `applied`, `failed`, `rolled_back`)
- `artifact_path`
- `checksum`
- `created_at`

7. ApplyOperation
- `id`
- `revision_id`
- `started_at`, `finished_at`
- `result`
- `error_message`

## 5. Protocol Mapping

| Protocol | Backend Engine | Статус в MVP | Примечание |
|---|---|---|---|
| VLESS | sing-box | Обязательно | Основной TCP/TLS сценарий |
| Hysteria2 | sing-box | Обязательно | UDP-heavy сценарий |
| XHTTP | Xray | Обязательно | Отдельный runtime backend |

### 5.1 Protocol/Transport Compatibility Matrix (MVP)

| Protocol | Allowed Transport | Default Engine | Allowed Engine Preference |
|---|---|---|---|
| `vless` | `tcp`, `ws`, `grpc` | `sing-box` | `sing-box` |
| `hysteria2` | `udp` | `sing-box` | `sing-box` |
| `xhttp` | `xhttp` | `xray` | `xray` |

### 5.2 Engine Resolution

`inbound add` и другие use-case слои не должны хардкодить mapping в CLI handlers.
Правила выбора backend engine:
1. Принять вход: `inbound.type`, `inbound.transport`, `optional preferred_engine`.
2. Проверить, что protocol поддерживается в MVP.
3. Проверить, что transport совместим с protocol по матрице.
4. Если `preferred_engine` не задан — использовать `default engine` из матрицы.
5. Если `preferred_engine` задан — валидировать его по матрице; при несовместимости вернуть понятную ошибку.

Ограничение MVP:
- Resolver не содержит деталей runtime-renderer для sing-box/xray, только правила совместимости и выбора engine.

## 6. Runtime Layout

### 6.0 Installation Layout (MVP stage 11)

Целевой install layout для single-node Debian/Ubuntu:
- бинарники:
  - `/usr/local/bin/proxyctl`
  - `/usr/local/bin/sing-box`
  - `/usr/local/bin/xray`
- конфиг и runtime:
  - `/etc/proxy-orchestrator/proxyctl.yaml`
  - `/etc/proxy-orchestrator/runtime/*`
- state и backup:
  - `/var/lib/proxy-orchestrator/*`
  - `/var/backups/proxy-orchestrator/*`
- systemd units:
  - `/etc/systemd/system/proxyctl-sing-box.service`
  - `/etc/systemd/system/proxyctl-xray.service`
  - `/etc/systemd/system/proxyctl-caddy.service`
  - `/etc/systemd/system/proxyctl-nginx.service`

Принцип idempotent installer:
- повторный запуск не должен безусловно перезаписывать пользовательский `proxyctl.yaml`;
- системные unit-файлы считаются managed-артефактами installer-а и могут обновляться с backup.

### 6.1 Server Directories (MVP stage 7)

```text
/etc/proxy-orchestrator/
  proxyctl.yaml
  runtime/
    sing-box.json
    xray.json
    sing-box.preview.json
    xray.preview.json
    caddy/
      Caddyfile
      Caddyfile.preview
    nginx/
      nginx.conf
      nginx.conf.preview
    decoy-site/
      index.html
      assets/*

/var/lib/proxy-orchestrator/
  proxyctl.db
  subscriptions/
    <user-id>.txt
    <user-id>.base64
    <user-id>.json
    <user-id>.preview.txt
    <user-id>.preview.base64
    <user-id>.preview.json

/var/backups/proxy-orchestrator/
  sing-box.json.<timestamp>.bak
  xray.json.<timestamp>.bak
  Caddyfile.<timestamp>.bak
  nginx.conf.<timestamp>.bak
```

### 6.2 File placement responsibilities
- `internal/renderer/*`:
  - генерирует только in-memory артефакты (`RenderResult`) и не работает с файловой системой.
- `internal/reverseproxy/caddy`:
  - генерирует `Caddyfile` на основе app-config и enabled inbound-ов;
  - загружает static decoy assets из `templates/decoy-site`;
  - не выполняет apply/restart/systemd операции.
- `internal/reverseproxy/nginx`:
  - генерирует `nginx.conf` на основе app-config и enabled inbound-ов;
  - загружает static decoy assets из `templates/decoy-site`;
  - не выполняет apply/restart/systemd операции.
- `internal/runtime/layout`:
  - гарантирует наличие runtime-каталогов;
  - пишет файлы runtime-конфигов (`sing-box.json`, `xray.json`, `caddy/Caddyfile`, `nginx/nginx.conf`) атомарно;
  - пишет preview-файлы (`*.preview.json`, `caddy/Caddyfile.preview`, `nginx/nginx.conf.preview`);
  - перед перезаписью runtime-конфига создаёт backup предыдущей версии;
  - пишет subscription файлы (`txt/base64/json`) атомарно;
  - раскладывает static decoy-site assets в runtime dir атомарно.
- `internal/subscription/service`:
  - строит payload подписки и делегирует file-write в `internal/runtime/layout`;
  - `preview`-ветка не изменяет metadata `subscriptions` в SQLite.

### 6.3 Stage boundaries
- На этапе 7 выполняется только file generation в runtime layout.
- `systemctl restart`, `apply/switch/reload` не выполняются в `preview/render`.

### 6.4 Runtime prerequisites (MVP installer)

Обязательные зависимости хоста:
- `systemd` (управление runtime units);
- `sqlite3` (локальная state DB);
- `curl`, `tar`, `unzip`, `ca-certificates` (bootstrap installer и загрузка runtime binaries);
- reverse proxy runtime packages: `caddy`, `nginx`.

Обязательные runtime binaries:
- `proxyctl`;
- `sing-box`;
- `xray`.

Ограничение MVP:
- Docker runtime не используется (ADR-006), установка выполняется напрямую в host filesystem.

## 7. Предполагаемые systemd units
- `proxyctl-sing-box.service` — runtime sing-box.
- `proxyctl-xray.service` — runtime Xray.
- `proxyctl-caddy.service` — reverse proxy по умолчанию.
- `proxyctl-nginx.service` — альтернативный reverse proxy.
- `proxyctl-apply.service` (опционально) — единичные apply-операции через systemd-run/oneshot.

Правило запуска в MVP:
- Стартуются только units, требуемые активной конфигурацией.
- Caddy и Nginx взаимоисключающие (активен только один).

Границы service management для stage 8 (MVP substage 8a):
- `proxyctl apply` управляет только runtime units backend-ов протоколов (`proxyctl-sing-box.service`, `proxyctl-xray.service`).
- Provisioning/restart `proxyctl-caddy.service` и `proxyctl-nginx.service` остаются за рамками этого подэтапа.

## 8. Общий apply pipeline
1. `collect`
- Считать текущую конфигурацию и CLI input.

2. `build`
- Сгенерировать кандидата ревизии в `staging/<op-id>`.

3. `validate`
- Проверить domain-ограничения.
- Проверить валидность всех сгенерированных конфигов.
- Проверить занятость портов и конфликты сервисов.

4. `persist`
- Записать ревизию в SQLite как `validated`.
- Зафиксировать артефакты в `revisions/<rev>`.

5. `switch`
- Атомарно обновить symlink `active` на новую ревизию.

6. `reload/restart`
- Выполнить restart/reload затронутых units в корректном порядке.

7. `health-check`
- Базовый smoke-check локальных портов и статусов systemd.

8. `commit/rollback`
- Успех: `status=applied`.
- Ошибка: вернуть предыдущую ревизию, перезапустить сервисы, `status=rolled_back`.

### 8.1 Stage 8 (MVP substage 8a) — текущая реализация
Выявлено расхождение между целевым полным pipeline (`persist/switch/health-check`) и текущим этапом поставки.

Для минимального отклонения в stage 8 реализуется безопасный runtime pipeline:
1. `collect`
2. `build`
3. `backup` текущих runtime-файлов
4. `validate` (validator hooks)
5. `apply` (атомарная запись runtime-файлов)
6. `restart/reload` через `ServiceManager`
7. `rollback` runtime-файлов и повторный restart/reload при ошибке перезапуска

Ограничение текущего подэтапа:
- шаги `persist/switch/health-check` остаются в целевом дизайне и будут реализованы отдельным этапом без breaking changes контракта apply-orchestrator.

## 9. Стратегия масштабирования в multi-node

Цель: сохранить single-node UX, добавив управляющий control-plane позже.

1. Логическое разделение ролей
- Control node: хранение глобального состояния и планов apply.
- Managed node: локальное применение ревизий и runtime контроль.

2. Эволюция модели данных
- Все сущности уже содержат `node_id`/привязку к узлу (или готовы к добавлению без breaking changes).
- `ConfigRevision` делится на global revision и node revision.

3. Протокол взаимодействия
- В будущем: pull-модель агентом на managed node (проще в NAT/Firewall средах).

4. Принципы совместимости
- Команды single-node остаются и работают как частный случай `--node local`.

## 10. Точки расширения под Telegram bot
1. Command Gateway
- Отдельный слой адаптера, который маппит Telegram команды на те же use-case методы, что и CLI.

2. AuthZ Hook
- Проверка прав для опасных операций (`apply`, `rollback`, `restart`) до входа в orchestration слой.

3. Operation Events
- Публикация событий жизненного цикла apply (`started`, `validated`, `failed`, `rolled_back`, `applied`) для уведомлений.

4. Read-only API Surface
- Набор query-use-cases (`status`, `list revisions`, `last errors`) для безопасных команд бота.

## 11. Порядок реализации
1. Базовый CLI каркас и domain-модели.
2. Persistence (SQLite) + миграции схемы.
3. Config Builder для VLESS/Hysteria2(X sing-box) и XHTTP(Xray).
4. Validation Engine.
5. Apply Orchestrator с атомарным `active` switch и rollback.
6. Runtime Control через systemd.
7. Reverse proxy integration (Caddy default, Nginx optional) + decoy site wiring.
8. Hardening: аудит операций, smoke-check, отказоустойчивые сценарии.
9. Подготовка интерфейсов расширения под multi-node и Telegram adapter.

## 12. CLI Surface (MVP skeleton)
Топ-уровень:
- `proxyctl init`
- `proxyctl status`
- `proxyctl user`
- `proxyctl node`
- `proxyctl inbound`
- `proxyctl render`
- `proxyctl validate`
- `proxyctl apply`
- `proxyctl subscription`
- `proxyctl logs`
- `proxyctl doctor`

Минимальные вложенные группы для каркаса:
- `user`: `list`, `add`, `remove`
- `node`: `list`, `show`
- `inbound`: `list`, `add`, `disable`
- `subscription`: `generate`, `show`, `export`
- `logs`: `runtime`, `apply`

Примечание:
- На этапе каркаса команды могут быть заглушками, но command shape фиксируется как контракт CLI-слоя.

### 12.1 Operational diagnostics scope (stage 12)
- `proxyctl status`:
  - показывает наличие SQLite DB и её инициализацию;
  - показывает наличие runtime-файлов в layout (`sing-box.json`, `xray.json`, reverse proxy config);
  - показывает статусы systemd units (`proxyctl-sing-box.service`, `proxyctl-xray.service`, `proxyctl-caddy.service`, `proxyctl-nginx.service`);
  - показывает выбранный reverse proxy и counters (`users`, `nodes`, `inbounds`).
- `proxyctl logs`:
  - `runtime`: `journalctl` для `sing-box`, `xray` и активного reverse proxy unit;
  - `apply`: `journalctl` для `proxyctl-apply.service` (если используется как operational unit).
- `proxyctl doctor`:
  - проверяет типовые проблемы эксплуатации: отсутствующие runtime files, отсутствующий reverse proxy config, пустой user-set, битые/неконсистентные пути, неинициализированную DB.

Ограничение:
- Диагностические команды read-only и не выполняют `apply/restart` действий.

## 13. Module-to-Package Mapping (MVP)
- CLI Layer -> `cmd/proxyctl`, `internal/cli`
- Domain Layer -> `internal/domain`
- Config Builder -> `internal/renderer`
- Validation Engine -> `internal/runtime/apply` (validator hooks в runtime apply pipeline)
- Apply Orchestrator -> `internal/runtime/apply`
- Persistence -> `internal/storage`
- Runtime Control -> `internal/runtime`
- Subscription Outputs -> `internal/subscription`
- Reverse Proxy Layer -> `internal/reverseproxy`

## 14. Renderer Layer (уточнение контрактов)

Renderer принимает только domain-данные и не зависит от CLI/parsing:
- вход: `Node`, `Inbound`, `Credential`;
- выход:
  - runtime artifact (`sing-box.json` или `xray.json`);
  - JSON preview для dry-run/inspect;
  - client artifacts (URI или структурированное представление для subscription).

Границы:
- resolver (`internal/engine`) решает совместимость protocol/transport/engine;
- renderer не выбирает engine, а рендерит только поддерживаемые inbounds для конкретного backend;
- renderer не делает apply/restart/systemd операции.

### 14.1 Protocol Mapping (sing-box MVP scope)
- Поддерживаемые inbound protocol в renderer MVP:
  - `vless`
  - `hysteria2`
- `xhttp` в sing-box renderer не поддерживается и остаётся в ветке Xray renderer.

### 14.2 Client Artifacts
- VLESS: генерируется URI-представление.
- Hysteria2: генерируется URI-представление (или эквивалентный клиентский payload для subscription layer).
- XHTTP/Xray: генерируется URI-представление на базе `vless://` с `type=xhttp`.
- Формирование клиентских артефактов выполняется в renderer слое из domain-модели и credential-связок.

### 14.3 Validator Hook
- Для renderer backend вводится интерфейс `Validator`.
- В MVP допустим `noop` validator.
- На следующих этапах реализация сможет вызывать `sing-box check` / `xray -test` в validate/apply pipeline.

### 14.4 Protocol Mapping (xray MVP scope)
- Поддерживаемые inbound protocol в renderer MVP:
  - `xhttp`
- Контракт backend-рендеринга для `xhttp`:
  - inbound рендерится в Xray-конфиг (`xray.json`) с protocol `vless` и `streamSettings.network = xhttp`;
  - credential kind для MVP: `uuid`;
  - renderer рендерит только inbounds с `engine = xray`, остальные игнорируются.

## 15. Subscription Service (MVP scope)

Назначение:
- Единый слой `internal/subscription/service`, агрегирующий client artifacts из renderer backend-ов (`sing-box`, `xray`) в пользовательскую подписку.

Входные данные:
- `User` (один пользователь);
- набор `Credential` пользователя;
- связанные `Inbound` (один пользователь может иметь несколько endpoint-ов);
- `Node` для host/endpoint-контекста.

Выходные форматы:
- `txt`: plain-text список URI (по одной строке);
- `base64`: base64-кодировка `txt` payload;
- `json`: структурированный export для будущей Telegram automation/adapter слоя.

CLI контракт MVP:
- `proxyctl subscription generate <user>`;
- `proxyctl subscription show <user>`;
- `proxyctl subscription export <user> --format json`.

Ограничения MVP:
- Локальная генерация/чтение из data dir;
- без HTTP delivery endpoint;
- без Telegram bot runtime;
- без remote nodes orchestration.

## 16. Storage/Data Flow for subscriptions

1. Service читает состояние из SQLite:
- `users`, `nodes`, `inbounds`, `credentials`.

2. Service фильтрует `Credential` по пользователю и связывает их с inbound-ами:
- один пользователь может иметь несколько credential/inbound связок;
- подписка может содержать несколько протоколов (`vless`, `hysteria2`, `xhttp`).

3. Service вызывает renderer-ы:
- `sing-box renderer` -> client artifacts для `engine=sing-box`;
- `xray renderer` -> client artifacts для `engine=xray`.

4. Service агрегирует URI и формирует `txt/base64/json`.

5. Service сохраняет файлы в `Paths.Subscription` (MVP: `/var/lib/proxy-orchestrator/subscriptions`).

6. Service обновляет таблицу `subscriptions`:
- `user_id`, последний `format`, `output_path`, `updated_at`.

## 17. Reverse Proxy Layer (Stage 9-10)

Назначение:
- Отдельный слой reverse proxy, не смешанный с protocol renderers (`sing-box`/`xray`).
- Базовая реализация MVP: `Caddy` как default backend, `Nginx` как optional backend (ADR-004).

Контракт:
- вход: `AppConfig` + выбранный `Node` + enabled `Inbound` для node;
- выход:
  - `Caddyfile` или `nginx.conf` (runtime/preview) в зависимости от backend selection;
  - список reverse-proxy routes для HTTP-транспортов;
  - decoy-site static assets для runtime layout.

Ограничения этапа:
- Caddy остаётся default (смена default не допускается);
- форки Nginx не входят в MVP;
- installer/provisioning Caddy/Nginx не входит в scope;
- restart/reload `proxyctl-caddy.service` и `proxyctl-nginx.service` остаётся вне stage 9/10.

### 17.1 Config selection between proxy backends

Источник выбора backend:
- `AppConfig.ReverseProxy` (YAML ключ `reverse_proxy`): `caddy` или `nginx`.

Правило выбора:
1. Если ключ отсутствует, используется default `caddy`.
2. Если задано `nginx`, `render/preview` генерируют только nginx runtime/preview файлы.
3. Если задано `caddy`, `render/preview` генерируют только caddy runtime/preview файлы.
4. Decoy assets раскладываются в обоих режимах одинаково (`runtime/decoy-site`).
5. Одновременная генерация обоих backend-конфигов в одном запуске не выполняется.

## 18. Decoy Site

MVP-дизайн:
- decoy site хранится как static assets в `templates/decoy-site`;
- при `render/preview` assets раскладываются в runtime: `/etc/proxy-orchestrator/runtime/decoy-site`;
- Caddy обслуживает decoy как default `file_server` контент для unmatched запросов.

Цель:
- скрыть proxy endpoint-ы за правдоподобным статическим сайтом.

## 19. Public Endpoint Layout

Базовый layout для reverse proxy backend (Caddy default, Nginx optional):
1. Публичный endpoint:
- Caddy: `https://<domain>` (или `http://<domain>`, если HTTPS disabled в app-config).
- Nginx: `http://<domain>` в MVP stage 10 (TLS provisioning остаётся за рамками этапа).

2. Маршрутизация:
- `/<path>` и `/<path>/*` для inbound-ов с transport `ws|grpc|xhttp` -> `127.0.0.1:<inbound.port>`.
- `grpc` маршруты:
  - Caddy: через `h2c` transport.
  - Nginx: через `grpc_pass`.

3. Fallback:
- все прочие запросы обслуживаются из `runtime/decoy-site`:
  - Caddy: `file_server`;
  - Nginx: `try_files ... /index.html`.

Источник домена:
- `AppConfig.Public.Domain` (если задан) -> иначе `Inbound.Domain` -> иначе `Node.Host`.
