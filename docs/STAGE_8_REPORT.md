# Stage 8 Report: validate/apply/restart/rollback pipeline (MVP substage 8a)

## Что реализовано
- Добавлен пакет `internal/runtime/apply`:
  - оркестратор `Orchestrator` с pipeline:
    - `collect` (из SQLite state через `storage.Store`);
    - `build` (рендер `sing-box`/`xray`);
    - `backup` текущих runtime-файлов;
    - `validate` через `ProcessValidator` hooks;
    - `apply` (атомарная запись runtime-файлов);
    - `restart/reload` через `ServiceManager`;
    - `rollback` файлов + повторный restart/reload при failed restart.
  - интерфейсы:
    - `ProcessValidator`;
    - `ServiceManager`.
  - MVP validator:
    - `JSONValidator` (проверка синтаксической валидности JSON runtime-конфигов).
- Добавлена systemd-реализация `ServiceManager`:
  - `internal/runtime/systemd/manager.go`;
  - вызовы `systemctl restart|reload <unit>` с диагностикой stderr в ошибке.

## Изменения CLI
- Реализована команда `proxyctl validate`:
  - запускает validate-only pipeline без side-effects на runtime files/systemd.
- Реализована команда `proxyctl apply`:
  - запускает apply pipeline;
  - поддерживает `--dry-run` (валидация без записи файлов/рестартов).

## Поведение rollback
- Если restart/reload хотя бы одного unit завершается ошибкой:
  - runtime-файлы восстанавливаются из pre-apply состояния;
  - повторно выполняется restart/reload затронутых unit’ов;
  - возвращается диагностическая ошибка с указанием failed step.

## Тесты
- Добавлены unit tests `internal/runtime/apply/orchestrator_test.go`:
  - успешный apply;
  - rollback при failed restart;
  - dry-run без side-effects.
- Прогон тестов:
  - `GOCACHE=/tmp/go-build go test ./...` — успешно.

## Расхождение с архитектурой и выбранная реализация
- Обнаружено расхождение:
  - общий pipeline в `ARCHITECTURE.md` включает `persist/switch/health-check`;
  - текущая задача этапа 8 требует runtime-safe orchestration `validate/apply/restart/rollback`.
- Выбран минимально отклоняющийся путь:
  - реализован подэтап `8a` без отмены целевого pipeline;
  - зафиксированы границы в документации (этап 8a покрывает runtime apply safety, а `persist/switch/health-check` остаются следующей поставкой).

## Что не делали (по требованиям)
- Не реализовывали `install.sh`.
- Не реализовывали reverse proxy provisioning.
- Не реализовывали multi-node SSH deploy.
