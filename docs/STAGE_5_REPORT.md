# Stage 5 Report: Xray Renderer (MVP scope)

## Что реализовано
- Добавлен пакет `internal/renderer/xray`:
  - typed renderer для server config (`xray.json`);
  - поддержка inbound:
    - `xhttp` (через Xray VLESS inbound + `streamSettings.network = xhttp`);
  - генерация client artifacts (URI) для subscription layer:
    - `vless://...` с `type=xhttp`;
  - встроенный JSON preview результата рендера;
  - интерфейс `Validator` + `NoopValidator` (hook под будущую проверку `xray -test`).
- Добавлены unit tests:
  - базовая генерация server/client outputs;
  - ошибка при отсутствии обязательных credential.
- Добавлены fixture-примеры в `examples/`:
  - `stage5_xray_request.json`;
  - `stage5_xray_server.json`;
  - `stage5_xray_clients.json`.

## Расхождение с документацией и выбранная реализация
- Обнаружено расхождение:
  - `docs/ROADMAP.md` этап 5 описывает Runtime интеграцию с systemd;
  - текущая задача требует Xray renderer.
- Выбран минимально отклоняющийся путь:
  - оформлен подэтап `5a` в roadmap (Xray renderer до полного runtime integration);
  - runtime/systemd/reverse proxy/apply не затрагивались.

## Архитектурные уточнения
- Обновлён `ARCHITECTURE.md`:
  - зафиксирован Xray MVP scope для renderer (`xhttp`);
  - зафиксирован формат runtime artifact (`xray.json`) и client artifact для XHTTP.

## Что не делали (по требованиям)
- Не смешивали Xray renderer и sing-box renderer.
- Не переносили domain logic в renderer.
- Не реализовывали реальный запуск Xray.
- Не реализовывали reverse proxy.
