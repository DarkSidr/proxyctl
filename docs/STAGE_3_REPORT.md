# Stage 3 Report: Protocol Compatibility + Engine Resolver

## Что реализовано
- Добавлен отдельный пакет `internal/engine`:
  - protocol/transport matrix для MVP;
  - resolver `Resolve(...)`, который принимает `protocol + transport + optional preferred engine` и возвращает итоговый engine;
  - понятные ошибки для несовместимых комбинаций.
- Подключён resolver в сценарий создания inbound (`proxyctl inbound add`):
  - если `--engine` не задан, engine назначается автоматически по matrix;
  - если `--engine` задан, выполняется валидация совместимости.
- Реализована команда `proxyctl inbound add` (раньше была заглушка).
- Расширен storage-контракт и SQLite-репозиторий для создания inbound (`Inbounds().Create(...)`).
- Добавлены unit tests для resolver (`internal/engine/resolver_test.go`):
  - совместимые комбинации;
  - несовместимые комбинации (protocol/transport/engine).

## Обновления документации
- `ARCHITECTURE.md`:
  - уточнён раздел `Protocol Mapping`;
  - добавлена явная таблица `Protocol/Transport Compatibility Matrix (MVP)`;
  - добавлены правила `Engine Resolution`.
- `docs/ROADMAP.md`:
  - добавлена пометка про подэтап `3a` (protocol compatibility + resolver до полного Validation Engine).

## Расхождение с документацией и выбранная реализация
- Обнаружено расхождение:
  - roadmap описывает этап 3 как полный Validation Engine;
  - текущая задача требует protocol compatibility + engine resolver.
- Выбран минимально отклоняющийся путь:
  - реализован подэтап `3a` как часть validation-логики (fail-fast на совместимости),
  - без выхода в `render/apply/runtime`.

## Проверки
- `gofmt -w ...` по изменённым Go-файлам.
- `GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod go test ./...` — успешно.

## Что не делали (по требованиям)
- Не реализовывали runtime render.
- Не реализовывали subscriptions.
- Не реализовывали installer.
- Не переносили детали renderer sing-box/xray в resolver.
