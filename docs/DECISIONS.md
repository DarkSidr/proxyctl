# Architectural Decisions (ADR)

## ADR-001: Язык реализации — Go
- Status: Accepted
- Date: 2026-03-13

### Context
CLI-оркестратор должен быть переносимым, простым в деплое на VPS и предсказуемым в эксплуатации без тяжелого runtime.

### Decision
Реализовать `proxyctl` на Go.

### Consequences
- Плюсы:
  - Один статически линкуемый бинарник для большинства target-сред.
  - Хорошая интеграция с системными процессами и файловыми операциями.
  - Быстрый старт и низкие операционные зависимости.
- Минусы:
  - Более строгая типизация и больший объем шаблонного кода, чем в скриптовых языках.

## ADR-002: CLI framework — Cobra
- Status: Accepted
- Date: 2026-03-13

### Context
Нужен структурированный CLI с подкомандами, help, валидацией флагов и расширяемой иерархией команд.

### Decision
Использовать Cobra как основной framework для CLI-команд.

### Consequences
- Плюсы:
  - Де-факто стандарт в Go CLI ecosystem.
  - Удобная декомпозиция команд для роста проекта.
  - Встроенные механизмы help/usage.
- Минусы:
  - Требует дисциплины в разделении command-layer и business-логики.

## ADR-003: Хранилище состояния MVP — SQLite
- Status: Accepted
- Date: 2026-03-13

### Context
MVP работает на одном сервере. Нужна локальная транзакционная БД для ревизий, статусов apply и аудита операций без отдельного DB-сервера.

### Decision
Использовать SQLite для persistence в MVP.

### Consequences
- Плюсы:
  - Нулевой операционный overhead.
  - ACID-транзакции для критичных операций apply/rollback.
  - Простое резервное копирование файла БД.
- Минусы:
  - Ограниченная модель конкурентной записи для multi-node control-plane в будущем.
  - При масштабировании потребуется миграционный путь на внешнюю БД.

## ADR-004: Reverse proxy по умолчанию — Caddy
- Status: Accepted
- Date: 2026-03-13

### Context
Для MVP нужен быстрый и безопасный default reverse proxy с минимальной ручной настройкой TLS и поддержкой decoy site.

### Decision
Сделать Caddy default backend; Nginx оставить опциональным.

### Consequences
- Плюсы:
  - Упрощенная default-конфигурация и быстрый time-to-first-deploy.
  - Меньше ручных шагов по TLS в типовом сценарии.
- Минусы:
  - Для части пользователей Nginx остается предпочтительным из-за привычных практик.
  - Нужно поддерживать два backend-пути в config builder.

## ADR-005: Отказ от web UI в MVP
- Status: Accepted
- Date: 2026-03-13

### Context
Требование MVP: self-hosted CLI-оркестратор без web UI, с фокусом на безопасный apply flow и быструю поставку рабочего ядра.

### Decision
Не реализовывать web UI в MVP; все операции доступны через CLI.

### Consequences
- Плюсы:
  - Ниже поверхность атаки.
  - Быстрее поставка ключевой функциональности.
  - Меньше эксплуатационных компонентов.
- Минусы:
  - Более высокий порог для пользователей без CLI-опыта.
  - Позднее потребуется отдельный UX-слой (например, bot/web) при росте требований.

## ADR-006: Runtime layout без Docker runtime
- Status: Accepted
- Date: 2026-03-13

### Context
На этапе file generation требуется предсказуемое размещение runtime-файлов на хосте, совместимое с systemd-сервисами и без дополнительного контейнерного слоя.

### Decision
- Использовать host-based layout:
  - `/etc/proxy-orchestrator`
  - `/etc/proxy-orchestrator/runtime`
  - `/var/lib/proxy-orchestrator`
  - `/var/lib/proxy-orchestrator/subscriptions`
  - `/var/backups/proxy-orchestrator`
- Не использовать Docker runtime в MVP pipeline.

### Consequences
- Плюсы:
  - Прозрачная эксплуатация и диагностика через стандартные пути Linux.
  - Прямая интеграция с systemd без контейнерного orchestration слоя.
  - Простая стратегия backup/restore runtime файлов.
- Минусы:
  - Требуется аккуратная работа с правами доступа и атомарной записью.
  - Контейнерная изоляция и portability остаются вне MVP.
