# TODO

## User limits and quotas

### 1) User subscription expiration (time-limited access)
- Add per-user expiration policy (`expires_at` or `duration_days`).
- Support presets at user creation: `1 day`, `3 days`, `7 days`, `30 days`, `unlimited`.
- When user is expired:
  - stop including user credentials in rendered runtime configs;
  - stop generating active subscription links;
  - show explicit status in CLI (`active` / `expired`).
- Add wizard actions:
  - set expiration;
  - extend expiration;
  - disable expiration.

### 2) Traffic limits per user (MB/GB)
- Add per-user traffic quota (`quota_bytes`) and usage counter (`used_bytes`).
- Allow limits in MB/GB at user creation and in user edit menu.
- When quota is reached:
  - mark user as `quota_exceeded`;
  - block new access via generated runtime/subscriptions until reset or extension.
- Add reset/extend operations:
  - reset usage;
  - increase quota;
  - set unlimited.

### 3) Accounting and enforcement model
- Define data source for usage accounting (xray/sing-box logs, metrics API, or local accounting layer).
- Implement periodic collector job (systemd timer / internal scheduler) to update `used_bytes`.
- Ensure counters are idempotent and safe after restart.
- Add anti-abuse policy for missing stats source (fail-open vs fail-closed, configurable).

### 4) CLI and wizard UX
- `user add`:
  - ask for expiration policy;
  - ask for traffic quota.
- `wizard -> users -> open user`:
  - show limits + current usage;
  - provide quick actions (`extend`, `reset`, `set unlimited`).
- `show configs` should display user status reason: `expired` or `quota exceeded`.
- Improve node selection label in inbound creation wizard:
  - show node `name` + `host` + `role` (for example `primary/secondary`) instead of only ID/host.
  - keep labels unambiguous when multiple nodes share similar hosts.

### 5) Storage and migrations
- Add DB fields to `users` table:
  - `expires_at` (nullable);
  - `quota_bytes` (nullable/0 for unlimited);
  - `used_bytes` (default 0);
  - `limit_state` (active/expired/quota_exceeded).
- Add migrations for existing installations without data loss.

### 6) Safety and observability
- Add warnings in `proxyctl diagnostics` for users near expiration/quota.
- Add audit events for limit changes (who/when/what changed).
- Add tests for edge cases:
  - exact quota boundary;
  - timezone/date boundary;
  - expired + disabled user interactions;
  - migration from old schema.

### 7) Wizard server monitoring and total traffic info
- Add lightweight server resource monitoring in wizard header/status area:
  - CPU usage;
  - RAM usage;
  - disk usage (root partition);
  - network RX/TX (current session snapshot).
- Add total traffic summary line directly in wizard screen (without separate menu item), for example:
  - `Total traffic used: 12.4 GB`.
- Show this line on every wizard entry/update so operator sees usage immediately.
- Ensure values are readable with auto units (`MB`/`GB`/`TB`) and rounded consistently.
- Keep data collection cheap and non-blocking to avoid slowing down interactive prompts.

## Additional ideas (assistant suggestions, not user-requested)

### A1) Safe operation mode
- Add `dry-run` for destructive/critical flows (`apply`, delete user, delete config) with preview of planned changes.
- Require explicit confirmation for high-impact operations and log confirmations.

### A2) Backups and rollback
- Create automatic backup snapshot before each `apply`.
- Add one-command rollback to previous successful snapshot.
- Keep retention policy (for example, last 10 snapshots).

### A3) Health dashboard
- Show service state (`sing-box`, `xray`, `caddy`, `nginx`) and last restart reason.
- Display certificate expiration countdown and warnings for near-expiry certs.
- Show last successful render/apply timestamp.

### A4) Access control and audit
- Introduce simple RBAC roles (`admin`, `operator`, `read-only`) for future API/web access.
- Add audit log entries for all mutations (who, when, what changed).

### A5) Import/export and migration
- Add `export/import` commands for users, inbounds, credentials, and subscriptions metadata.
- Validate imported payload with schema checks before applying.

### A6) Metrics and alerting
- Expose Prometheus-style metrics for runtime status and usage.
- Add basic alert rules: service down, quota near limit, subscription generation failures.
