# Stage 4 Report: sing-box Renderer (MVP scope)

## Что реализовано
- Добавлен пакет `internal/renderer/singbox`:
  - typed renderer для server config (`sing-box.json`);
  - поддержка inbound:
    - `vless`;
    - `hysteria2`;
  - генерация client artifacts (URI):
    - `vless://...`;
    - `hysteria2://...`;
  - встроенный JSON preview результата рендера;
  - интерфейс `Validator` + `NoopValidator` (hook под будущий `sing-box check`).
- Обновлён общий контракт renderer (`internal/renderer/renderer.go`):
  - вход строится из domain-модели (`Node`, `Inbound`, `Credential`);
  - выход включает runtime artifacts, JSON preview и client artifacts.
- Добавлены unit tests:
  - базовая генерация server/client outputs;
  - ошибка при отсутствии обязательных credential.
- Добавлены fixture-примеры в `examples/`:
  - `stage4_singbox_request.json`;
  - `stage4_singbox_server.json`;
  - `stage4_singbox_clients.json`.

## Расхождение с документацией и выбранная реализация
- Обнаружено расхождение:
  - `docs/ROADMAP.md` этап 4 описывает Apply Orchestrator;
  - текущая задача требует sing-box renderer.
- Выбран минимально отклоняющийся путь:
  - оформлен подэтап `4a` в roadmap (renderer до полного apply);
  - runtime/apply/restart/installer не затрагивались.

## Архитектурные уточнения
- Обновлён `ARCHITECTURE.md`:
  - зафиксированы границы Renderer Layer;
  - уточнён sing-box MVP protocol mapping (`vless`, `hysteria2`);
  - добавлены client artifacts и validator hook как контракты renderer слоя.

## Что не делали (по требованиям)
- Не реализовывали Xray renderer.
- Не реализовывали apply/restart.
- Не реализовывали reverse proxy/installer.

