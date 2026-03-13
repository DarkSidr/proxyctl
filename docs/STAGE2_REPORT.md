# Stage 2 Report: Domain Model + Storage (MVP)

## Что сделали
- Реализовали domain-сущности: `User`, `Node`, `Inbound`, `Credential`, `Subscription`.
- Добавили storage-контракты (репозитории) и интерфейс `SecretStore` как extension point на будущее.
- Реализовали SQLite storage слой:
  - `Open()` для подключения к БД;
  - `Init()` для bootstrap схемы;
  - репозитории для `users`, `nodes`, `inbounds`, `credentials`, `subscriptions`.
- Добавили автоинициализацию схемы БД для MVP (`CREATE TABLE IF NOT EXISTS`, индексы, FK).
- Подключили CLI-команды:
  - `proxyctl init`
  - `proxyctl user add`
  - `proxyctl user list`
  - `proxyctl node add`
  - `proxyctl node list`
  - `proxyctl inbound list`
- Добавили глобальный флаг `--db` для явного пути к SQLite файлу.
- Внесли минимальные уточнения в документацию (`ARCHITECTURE.md`, `docs/ROADMAP.md`) без выхода за рамки этапа.

## Какие проверки прошли
- `go build ./...` — успешно.
- Проверка сценария на локальной БД (`/tmp/proxmax.db`) — успешно:
  - `proxyctl init`
  - `proxyctl user add --name alice`
  - `proxyctl user list`
  - `proxyctl node add --name eu-1 --host 1.2.3.4`
  - `proxyctl node list`
  - `proxyctl inbound list`
- Разделение слоёв соблюдено: CLI вызывает storage/domain, без смешивания с renderer/runtime/apply.

## Трудности и как решали
- Расхождение roadmap и текущего запроса:
  - в `docs/ROADMAP.md` этап 2 описан как Config Builder;
  - по фактической задаче требовался domain+storage.
  - Решение: реализован persistence/domain-подэтап и добавлена явная пометка в roadmap.
- Ограничения sandbox при сборке Go:
  - недоступен системный кэш `~/.cache/go-build` и сетевой доступ по умолчанию;
  - использовали `GOCACHE=/tmp/go-build`, `GOMODCACHE=/tmp/go-mod` и запуск с разрешением на загрузку зависимостей.
- Выбор SQLite-драйвера:
  - первоначальный вариант с `modernc.org/sqlite` вёл к нестабильности по версии Go;
  - переключились на `github.com/mattn/go-sqlite3`, после чего сборка и CLI-проверки прошли стабильно.
