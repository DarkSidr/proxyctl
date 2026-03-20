# proxyctl — контекст для Claude

## Что это

**proxyctl** — self-hosted CLI control-plane для управления VPN/прокси-стеком на VPS.
Управляет несколькими удалёнными узлами через SSH, генерирует конфиги для прокси-движков,
синхронизирует их на серверы и управляет systemd-сервисами.

Пользователи — люди, которые поднимают собственный VPN и хотят удобно им управлять
без веб-панели, через терминал или встроенную веб-панель (panel).

## Языки и стек

- **Go 1.23+**, модуль `proxyctl`
- **SQLite** (через `go-sqlite3`) — хранилище состояния (nodes, inbounds, users, credentials, subscriptions)
- **cobra** — CLI-фреймворк
- Деплой через `install.sh` на Debian 12/13, Ubuntu 22.04/24.04

## Прокси-движки на узлах

| Движок | Протоколы | Юнит systemd |
|--------|-----------|--------------|
| sing-box | hysteria2, vless (частично) | `proxyctl-sing-box.service` |
| xray | vless (tcp/ws/grpc), xhttp | `proxyctl-xray.service` |
| caddy | reverse proxy + ACME-сертификаты | `proxyctl-caddy.service` |
| nginx | альтернативный reverse proxy | `proxyctl-nginx.service` |

## Поддерживаемые протоколы / транспорты

```
vless   + tcp / ws / grpc  → xray
hysteria2 + udp            → sing-box  (TLS обязателен, сертификат берёт caddy)
xhttp   + xhttp            → xray
```

## Пути по умолчанию на узлах

```
/etc/proxy-orchestrator/              — base dir
  runtime/
    sing-box.json                     — конфиг sing-box (генерируется proxyctl)
    xray.json                         — конфиг xray
    caddy/Caddyfile                   — конфиг caddy (генерируется proxyctl при sync)
  proxyctl.yaml                       — конфиг приложения
/var/lib/proxy-orchestrator/
  proxyctl.db                         — SQLite база
  subscriptions/public/               — файлы подписок
/caddy/certificates/acme-v02.api.letsencrypt.org-directory/{domain}/
  {domain}.crt / {domain}.key         — ACME-сертификаты, которые читают sing-box и xray
```

## Структура кода

```
internal/
  domain/domain.go          — доменные модели: Node, Inbound, User, Credential, Subscription
  config/model.go           — AppConfig, пути, юниты, режим деплоя
  config/loader.go          — загрузка proxyctl.yaml
  engine/resolver.go        — матрица протокол→движок (Resolve)
  storage/sqlite/
    schema.go               — CREATE TABLE + миграции (schemaMigrations slice)
    repositories.go         — CRUD для всех сущностей
  renderer/
    singbox/renderer.go     — генерация sing-box.json
    xray/renderer.go        — генерация xray.json
  reverseproxy/
    caddy/builder.go        — генерация Caddyfile (включая bare-домены для ACME)
    nginx/builder.go        — генерация nginx.conf
  cli/
    panel_cmd.go            — веб-панель (~6000 строк, всё в одном файле)
                              встроенный HTTP-сервер на Go, HTML/JS через template literal
    node_remote.go          — SSH-синхронизация узлов (syncSingleNode, SCP, systemctl)
    commands.go             — wizard-команды CLI (не panel)
    diagnostics.go          — proxyctl doctor / status
    synced_inbounds.go      — снапшот инбаундов для узла
  subscription/service/     — генерация файлов подписок (clash/sing-box/json)
  runtime/
    layout/layout.go        — файловая раскладка артефактов
    apply/orchestrator.go   — apply pipeline (staging → active)
    systemd/manager.go      — обёртка над systemctl
```

## Веб-панель (panel_cmd.go)

- Команда: `proxyctl panel`
- Встроенный HTTP-сервер, всё UI — inline HTML+JS в Go-шаблоне
- Polling каждые ~5 сек: `GET /api/snapshot` → JS перерисовывает DOM
- Глобальный `let snapshot = null;` — доступен во всех event-хэндлерах
- Event-делегация на `#inboundsBody` (не per-element listeners) — важно для edit-модала
- `panelInboundView` — структура данных для JS (JSON в snapshot)
- `panelInboundVersion()` — sha256-хэш инбаунда для детектирования изменений
- Синхронизация узла после любого изменения: `panelSyncWorkerNodesByIDs()`

## Синхронизация узла (node_remote.go)

`syncSingleNode()` при каждом вызове:
1. Рендерит `sing-box.json` и `xray.json`
2. Строит `Caddyfile` (caddy.Builder.Build) — включает все домены с TLS (hysteria2 тоже)
3. SCP все файлы во временную директорию на узле
4. SSH: устанавливает файлы в runtime-директории
5. `systemctl restart` sing-box и xray
6. `systemctl reload-or-restart caddy` (если есть Caddyfile)

## Миграции БД

Новые поля добавляются через `schemaMigrations` в `schema.go` (ALTER TABLE).
Миграции идемпотентны — движок пропускает уже применённые.

## TLS и ACME-сертификаты

- Caddy автоматически получает сертификаты Let's Encrypt для всех доменов в Caddyfile
- sing-box и xray берут сертификат из `/caddy/certificates/.../domain.crt`
- `needsCaddyCert(inbound)`: TLSEnabled=true && RealityEnabled=false && TLSCertPath==""
- Reality (vless+tcp+xray) использует собственную пару ключей, сертификат caddy не нужен

## Типичные задачи

- **Добавить поле в инбаунд**: domain.go → schema.go (migration) → repositories.go → renderers → panel_cmd.go (struct + form + snapshot)
- **Изменить генерацию конфига**: renderer/singbox или renderer/xray
- **Добавить команду в панель**: panel_cmd.go (handler + UI)
- **Изменить что синкается на узел**: node_remote.go / syncSingleNode

## Версионирование

- Семантическое: `v0.1.NNN`
- Текущая версия: v0.1.142
- Теги в git: `git tag vX.Y.Z` после коммита

## Рабочий процесс (обязательно)

После каждой завершённой задачи **всегда предлагай сделать коммит**:
1. `git add <изменённые файлы>`
2. Коммит с внятным сообщением (тип: описание)
3. Новый тег `v0.1.NNN` (следующий после последнего существующего — проверяй через `git tag | sort -V | tail -3`)
4. `git push origin main --tags`
