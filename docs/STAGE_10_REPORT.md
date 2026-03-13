# Stage 10 Report: Nginx as optional reverse proxy backend

## Что реализовано
- Добавлен альтернативный reverse proxy backend `internal/reverseproxy/nginx`:
  - генерация `nginx.conf` из `AppConfig` + выбранного `Node` + enabled `Inbound`;
  - поддержка transport `ws`, `grpc`, `xhttp`;
  - доменное правило выбора сохранено: `AppConfig.Public.Domain -> Inbound.Domain -> Node.Host`;
  - fallback на decoy site через `try_files`.
- Добавлен шаблон:
  - `templates/nginx/nginx.conf.tmpl`.
- Сохранён отдельный Caddy flow:
  - `internal/reverseproxy/caddy` не изменён по контракту default backend.

## Выбор backend через app config
- Добавлена загрузка YAML-конфига `internal/config/loader.go`:
  - ключ `reverse_proxy: caddy|nginx`;
  - fallback на `DefaultAppConfig`, если файл конфига отсутствует;
  - валидация неподдерживаемых значений.
- Команды `proxyctl render`, `proxyctl preview`, `proxyctl validate`, `proxyctl apply` теперь читают `--config` и используют загруженный `AppConfig`.
- Если флаг `--db` не передан, используется `storage.sqlite_path` из app config.

## Runtime layout изменения
- Расширен `internal/runtime/layout`:
  - новый каталог `runtime/nginx`;
  - запись `nginx/nginx.conf` с backup;
  - запись `nginx/nginx.conf.preview`;
  - `EnsureDirectories` теперь создаёт `NginxDir`.
- Caddy runtime layout сохранён без регрессий.

## Decoy site
- Decoy site поддержан в обоих backend-ах:
  - `render/preview` раскладывают те же assets в `runtime/decoy-site`;
  - backend выбирается из app config, decoy flow остаётся единым.

## Документация
- Обновлён `ARCHITECTURE.md`:
  - runtime layout дополнен `nginx/`;
  - уточнён Reverse Proxy Layer (stage 9-10);
  - добавлен явный раздел выбора backend через `AppConfig.ReverseProxy`.
- Обновлён `docs/ROADMAP.md`:
  - добавлен этап 10 с ограничениями (Nginx optional, Caddy default).
- Обновлены:
  - `README.md` — инструкция по переключению `reverse_proxy`;
  - `examples/proxyctl.yaml` — пример app config;
  - `examples/README.md` — ссылка на пример app config.

## Обнаруженные расхождения и выбранная реализация
- Обнаружено расхождение: в `ARCHITECTURE.md` раздел stage 9 фиксировал ограничение "Nginx path не реализуется".
- Выбран минимально отклоняющийся путь:
  - обновлён архитектурный раздел до stage 9-10 без изменения принятых решений ADR-004;
  - сохранено правило "Caddy default, Nginx optional".

## Тесты
- Прогон: `GOCACHE=/tmp/go-build GOMODCACHE=/tmp/go-mod-cache go test ./...`
- Результат: успешно.

## Что не делали (по требованиям)
- Не меняли default backend с Caddy на Nginx.
- Не добавляли форки Nginx.
- Не добавляли provisioning/restart reverse proxy units в apply pipeline.
