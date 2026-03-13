# Stage 6 Report: Subscription Service (MVP substage 6a)

## Что реализовано
- Добавлен пакет `internal/subscription/service`:
  - агрегация `ClientArtifact` из `sing-box` и `xray` renderer-ов;
  - поддержка форматов подписки:
    - `txt` (plain URI list),
    - `base64` (кодировка txt payload),
    - `json` (структурированный export для автоматизации);
  - сохранение сгенерированных файлов в data dir (`Paths.Subscription`);
  - обновление `subscriptions` metadata в SQLite;
  - чтение последнего сгенерированного payload (`show`).
- Расширен storage контракт:
  - `CredentialRepository.List(ctx)` + SQLite реализация.
- Добавлены CLI команды:
  - `proxyctl subscription generate <user>`;
  - `proxyctl subscription show <user>`;
  - `proxyctl subscription export <user> --format json`.
- Добавлены тесты `internal/subscription/service/service_test.go`:
  - генерация подписки из нескольких inbound-ов и нескольких протоколов;
  - проверка `txt/base64/json` outputs;
  - проверка обновления формата в `subscriptions` при export.

## Расхождение с документацией и выбранная реализация
- Обнаружено расхождение:
  - `docs/ROADMAP.md` этап 6 описан как Hardening MVP;
  - текущая задача этапа 6 требует Subscription Service.
- Выбран минимально отклоняющийся путь:
  - добавлен подэтап `6a` (subscription service) до полного hardening;
  - hardening-содержимое этапа 6 не затрагивалось.

## Архитектурные уточнения
- Обновлён `ARCHITECTURE.md`:
  - зафиксирован контракт Subscription Service;
  - зафиксированы output форматы (`txt/base64/json`);
  - зафиксирован storage/data flow для subscriptions;
  - уточнена CLI surface для `subscription` (`generate/show/export`).
- Обновлён `docs/ROADMAP.md`:
  - добавлен подэтап `6a` для subscription service.

## Что не делали (по требованиям)
- Не реализовывали Telegram bot.
- Не реализовывали remote nodes.
- Не реализовывали HTTP delivery endpoint.
