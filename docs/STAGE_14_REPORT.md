# Stage 14 Report: финальная инженерная полировка и testing checklist

## Что сделано
- Проведён аудит репозитория на консистентность кода/документации без изменения архитектуры MVP.
- Добавлен `TESTING.md` с ручным smoke checklist для основного flow:
  - `install`
  - `init`
  - `add user`
  - `add inbound vless`
  - `add inbound hysteria2`
  - `add inbound xhttp`
  - `generate subscription`
  - `validate`
  - `apply`
  - `status`
  - `logs`
- Добавлен `Makefile` для локальных инженерных проверок:
  - `make build`
  - `make test`
  - `make vet`
  - `make fmt-check`
  - `make check`
  - `make smoke-help`
- Исправлено слабое user-facing сообщение с `TODO` в CLI-заглушках:
  - теперь возвращается явное `command "<path>" is not implemented in MVP`.
- Обновлён `README.md`:
  - синхронизирован с фактическим CLI;
  - добавлены локальные команды проверки через `make`;
  - добавлена ссылка на `TESTING.md`.
- Обновлён `docs/ROADMAP.md`:
  - добавлен этап 14;
  - выровнен краткий порядок реализации до текущего состояния roadmap.

## Найденные расхождения и решение
- Расхождение 1: в запросе указан `docs/STAGE_13_REPORT.md`, но файл отсутствует в репозитории.
  - Выбранное решение: не реконструировать stage 13 искусственно; зафиксировать факт в отчёте этапа 14 и продолжить с минимальным отклонением от текущих документов.
- Расхождение 2: roadmap доработан до stage 12, при этом выполняется stage 14.
  - Выбранное решение: добавить stage 14 в `docs/ROADMAP.md` без изменения уже принятых ADR и архитектурных границ.

## Проверки
- `GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod go test ./...` — успешно.
- `go run ./cmd/proxyctl --help` — успешно.
- `go run ./cmd/proxyctl inbound add --help` — успешно.
- `go run ./cmd/proxyctl subscription --help` — успешно.

## Что не делали (по ограничениям этапа)
- Не добавляли новые продуктовые фичи.
- Не переписывали архитектуру.
- Не выходили за границы MVP, зафиксированные в `ARCHITECTURE.md` и `docs/DECISIONS.md`.
