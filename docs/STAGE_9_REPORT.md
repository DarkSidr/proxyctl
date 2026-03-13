# Stage 9 Report: Caddy default reverse proxy layer and decoy site

## Что реализовано
- Добавлен отдельный reverse proxy слой `internal/reverseproxy/caddy`:
  - генерация `Caddyfile` из `AppConfig` + выбранного `Node` + enabled `Inbound`;
  - поддержка HTTPS/HTTP endpoint address через `AppConfig.Public.HTTPS`;
  - выбор домена по правилу `AppConfig.Public.Domain -> Inbound.Domain -> Node.Host`;
  - маршрутизация backend endpoint-ов для transport `ws`, `grpc`, `xhttp` на `127.0.0.1:<port>`;
  - для `grpc` включён `h2c` transport в `reverse_proxy`.
- Добавлены шаблоны:
  - `templates/caddy/Caddyfile.tmpl`;
  - `templates/decoy-site/index.html`;
  - `templates/decoy-site/assets/style.css`.
- Добавлена загрузка и раскладка decoy assets из templates в runtime.

## Runtime layout (stage 9)
- Расширен `internal/runtime/layout`:
  - новые каталоги: `runtime/caddy`, `runtime/decoy-site`;
  - запись `caddy/Caddyfile` с backup;
  - запись `caddy/Caddyfile.preview`;
  - атомарная запись static decoy assets.
- Добавлены тесты `internal/runtime/layout/layout_test.go` для:
  - записи/backup Caddy config;
  - записи decoy assets.

## Изменения CLI
- Команда `proxyctl render` теперь дополнительно:
  - генерирует `caddy/Caddyfile`;
  - раскладывает decoy-site assets в runtime;
  - печатает путь Caddy config и число decoy файлов.
- Команда `proxyctl preview` теперь дополнительно:
  - генерирует `caddy/Caddyfile.preview`;
  - раскладывает decoy-site assets в runtime;
  - печатает путь Caddy preview.

## Документация и архитектура
- Обновлён `ARCHITECTURE.md`:
  - runtime layout дополнен `caddy/` и `decoy-site/`;
  - добавлены явные секции `Reverse Proxy Layer`, `Decoy Site`, `Public Endpoint Layout`;
  - module mapping дополнен `internal/reverseproxy`.
- Обновлён `docs/ROADMAP.md`:
  - добавлен этап 9 с ограничениями (без Nginx, без installer/provisioning).

## Обнаруженное расхождение и выбранная реализация
- Найдено расхождение в `ARCHITECTURE.md`: в разделе subscription service оставался legacy путь `/opt/proxyctl/subscription`, тогда как stage-7 layout уже зафиксировал `/var/lib/proxy-orchestrator/subscriptions`.
- Выбран минимально отклоняющийся путь:
  - обновлён путь в архитектурном документе до `/var/lib/proxy-orchestrator/subscriptions` без изменения текущего контракта subscription service.

## Тесты
- Прогон: `GOCACHE=/tmp/go-build go test ./...`.
- Результат: успешно.

## Что не делали (по требованиям)
- Не реализовывали Nginx.
- Не реализовывали installer/provisioning.
- Не добавляли нестандартные сценарии вне ТЗ.
