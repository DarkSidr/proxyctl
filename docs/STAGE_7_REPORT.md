# Stage 7 Report: Runtime Layout and File Generation (MVP substage 7a)

## Что реализовано
- Добавлен пакет `internal/runtime/layout`:
  - фиксированные директории stage 7:
    - `/etc/proxy-orchestrator`
    - `/etc/proxy-orchestrator/runtime`
    - `/var/lib/proxy-orchestrator`
    - `/var/lib/proxy-orchestrator/subscriptions`
    - `/var/backups/proxy-orchestrator`
  - `EnsureDirectories()`;
  - `WriteRenderedSingBoxConfig()`;
  - `WriteRenderedXrayConfig()`;
  - `BackupPreviousConfig()`;
  - `WriteSubscriptionFiles()`.
- Реализована атомарная запись файлов через `WriteAtomicFile()` (temp file + rename + fsync).
- Добавлены тесты `internal/runtime/layout/layout_test.go`:
  - создание директорий;
  - backup предыдущего конфига;
  - запись subscription файлов (включая suffix для preview);
  - атомарная перезапись файла.

## Изменения в CLI
- Команда `proxyctl render` теперь:
  - рендерит `sing-box`/`xray` конфиги из renderer layer;
  - пишет файлы в runtime layout;
  - делает backup предыдущих runtime-конфигов;
  - генерирует subscription файлы пользователей с credential bindings.
- Добавлена команда `proxyctl preview`:
  - создаёт preview-файлы конфигов (`*.preview.json`) и подписок (`*.preview.*`);
  - не выполняет `apply/restart`;
  - не изменяет metadata `subscriptions` в SQLite.

## Интеграция с subscription service
- `internal/subscription/service` переведён на запись файлов через `internal/runtime/layout`.
- Добавлен `Build(ctx, userRef)` для preview flow без персистентных side-effects.

## Расхождение с документацией и выбранная реализация
- Обнаружено расхождение:
  - текущий раздел `Этап 7` в `docs/ROADMAP.md` описывал extension points;
  - фактическая задача этапа — runtime layout и file generation.
- Выбран минимально отклоняющийся путь:
  - зафиксирован подэтап `7a` в `docs/ROADMAP.md` без удаления исходного этапа расширений.

## Архитектурные уточнения
- Обновлён `ARCHITECTURE.md`:
  - добавлены разделы `Runtime Layout`, `Server Directories`, `File placement responsibilities`;
  - зафиксирована граница этапа: `preview/render` без `apply/restart`.
- Обновлён `docs/DECISIONS.md`:
  - добавлен ADR-006 (host-based runtime layout, без Docker runtime).
- Обновлён `internal/config/model.go`:
  - дефолтные пути переведены на `/etc|/var/*/proxy-orchestrator`.

## Что не делали (по требованиям)
- Не выполняли `systemctl restart`.
- Не реализовывали `install.sh`.
- Не реализовывали provisioning `caddy/nginx`.
