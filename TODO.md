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
- Add holistic per-user traffic limit mode across all user access vectors:
  - one shared quota for user total traffic (all attached inbounds + all subscription-delivered configs);
  - enforce user-level block when total quota is exceeded, regardless of inbound/protocol.

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
- Add direct inbound enable/disable actions in wizard and CLI:
  - allow fast toggle without deleting inbound (`disable inbound` / `enable inbound`);
  - show clear enabled/disabled state in lists and details.
- Add subscription enable/disable controls:
  - allow disabling a user subscription profile without deleting files/metadata;
  - allow re-enable without full regenerate flow.
- Show user traffic usage in panel/CLI screens:
  - display total used traffic per user in user list/open user screens;
  - display per-inbound contribution when available.
- Include traffic usage info in subscription outputs:
  - embed usage/remaining quota metadata in subscription JSON (and optional TXT comment/header when supported);
  - keep format backward-compatible for clients that ignore extra metadata.
- Improve node selection label in inbound creation wizard:
  - show node `name` + `host` + `role` (for example `primary/secondary`) instead of only ID/host.
  - keep labels unambiguous when multiple nodes share similar hosts.
- Consider removing direct TLS prompt from default (quick) wizard flow:
  - keep safer default (`TLS = no`) for quick mode;
  - expose TLS toggle/edit only in advanced mode for power users.
- Improve SNI UX for Reality setup:
  - provide default SNI preset list (for example: `microsoft`, `nvidia`, `intel`, `amd` domains);
  - add optional random pick from presets in quick mode;
  - move direct `Reality server` input to advanced mode (keep quick mode minimal).
- Improve wizard apply flow for fresh setups:
  - before `apply`, check whether inbound has at least one attached user credential;
  - if no user/credential exists, offer guided actions (`create user` / `attach user`) instead of hard failure;
  - consider moving `Apply now` prompt out of quick mode (or default to deferred apply in quick flow).
- Improve post-attach UX in user menu:
  - after successful `attach to existing inbound`, avoid returning to the same action prompt by default;
  - prefer auto-return to parent menu or switch default action to `show configs` to prevent accidental duplicate attaches.
- Ensure runtime service persistence after successful apply:
  - when `apply` activates a runtime service (for example `proxyctl-xray.service`), also `enable` it for reboot persistence, not only restart/start.
- Add quick Caddy stub management entry in wizard main menu:
  - add dedicated menu item in root wizard screen for Caddy fallback/stub page;
  - allow fast open/edit/update without navigating deep into config flows;
  - keep it safe with preview/confirm before apply.
- Add wizard settings control for auto-update timer:
  - settings toggle to enable/disable `proxyctl-self-update.timer` without reinstall;
  - show current timer state (`enabled/active/next run`) directly in settings screen.
- Revisit default auto-update schedule:
  - evaluate fixed daily window (for example 03:00 local server time) vs randomized schedule;
  - expose schedule choice in settings for predictable maintenance windows.
- Add strict TLS guardrails in wizard for `engine=sing-box`:
  - if `TLS = yes`, do not allow saving inbound with empty/invalid certificate paths;
  - allow explicit auto-path mode (Caddy-managed cert paths) with existence validation before apply.
- Add strict Reality guardrails in wizard for `engine=sing-box`:
  - if `Reality = yes` and `engine=sing-box`, block immediately in wizard with clear message before final command run;
  - offer actionable fallback in prompt (`switch engine to xray` or `disable Reality`);
  - avoid late failure (`--reality requires --engine xray`) after full interactive input.
- Fix inconsistent `0) back` behavior in wizard selection submenus:
  - in some selectors (for example `Select inbound`) `0` is shown but rejected as invalid value;
  - unify back/cancel behavior across all `promptChoice`-based menus so `0` always exits to parent menu;
  - add regression tests for nested menus (`users -> open user -> attach to existing inbound`, and similar flows).
- Optimize auto-sync target scope after `apply`:
  - in `panel+node` mode, avoid syncing all worker nodes on every apply when only part of node set changed;
  - sync only affected `role=node` targets based on changed inbounds/credentials (node-aware diff);
  - keep explicit full sync available as manual action.

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

### 8) Reliability and operations hardening
- Add preflight checks before `apply` with explicit report:
  - port conflicts (host/runtime);
  - certificate/key file readability for TLS inbounds;
  - DNS/SNI sanity warnings for common misconfigurations.
- Add atomic apply with auto-rollback:
  - if restarted runtime services do not become `active` within timeout, rollback to last known-good runtime artifacts;
  - print rollback reason and recovery status.
- Add inbound templates/presets for fast setup:
  - `vless reality tcp`;
  - `xhttp tls`;
  - `hysteria2 tls`.
- Add credential rotation flow with grace window:
  - issue new secret/uuid while keeping old credential temporarily active;
  - auto-disable old credential after configurable TTL.
- Add host:port conflict guardrails:
  - block incompatible combinations by default;
  - require explicit confirmation for advanced overrides.
- Add client compatibility hints near generated URIs:
  - show recommended client apps per protocol/transport;
  - include minimal required options for each client class.
- Add configurable restart policy for `apply`:
  - restart only changed runtime services by default;
  - optional mode to restart all managed runtime services.
- Add one-command safe cleanup flow:
  - dry-run preview for deleting users/inbounds/credentials;
  - mandatory backup creation and easy restore pointer.

### 9) Runtime warnings cleanup (from production status check)
- Fix `proxyctl-caddy.service` environment so `HOME` is not empty:
  - set explicit `HOME` (for example `/var/lib/caddy`) in systemd unit;
  - ensure Caddy storage path is stable/persistent and not relative `./caddy`.
- Investigate and document OCSP stapling warning for active cert:
  - warning observed: `no OCSP server specified in certificate`;
  - verify whether current CA/cert chain intentionally lacks OCSP;
  - keep as non-blocking if TLS handshake/renewal is healthy.
- Plan migration away from deprecated Xray mode:
  - warning observed: `VLESS (with no Flow, etc.) is deprecated`;
  - add migration task to VLESS with Flow/Seed to avoid future breakage.

### 10) Multi-node subscriptions and remote node wrappers (new feature)
- Implement per-user unified subscription URL that can include multiple inbounds across different nodes.
- Add subscription profile model:
  - one profile per user with a stable token/link;
  - profile includes selected credentials/inbounds from multiple nodes.
- Extend node model for remote execution:
  - support node role/types (for example `control-plane`, `worker`, `edge`);
  - keep current host as primary control node and allow remote worker nodes for runtime only.
- Add remote wrapper agent concept for secondary hosts:
  - lightweight component on remote host that manages only `sing-box`/`xray` runtime files + service lifecycle;
  - no full control-plane DB/UI on workers.
- Define control channel from primary node to workers:
  - authenticated API or secure pull/push sync for rendered artifacts;
  - signed payloads and versioned snapshots.
- Support per-node render/apply pipeline:
  - render artifacts centrally on primary host;
  - deliver node-specific artifacts to each worker;
  - apply/restart only changed service on target node.
- Subscription merge/export behavior:
  - on `subscription generate/export`, aggregate URIs from all enabled user attachments across nodes;
  - include node tag/remark in URI names for client clarity.
- Failure and safety policy:
  - if one node is down, decide configurable behavior (`partial subscription` vs `strict fail`);
  - keep last known-good artifact on each worker with rollback support.
- Wizard/CLI tasks:
  - `node add/remove/list/test`;
  - attach inbound to remote node;
  - `subscription inspect <user>` showing node distribution.
- Security tasks:
  - mutual auth between control-plane and workers;
  - key rotation for node credentials;
  - minimal permissions on worker host (runtime-only scope).
- Observability tasks:
  - per-node apply status + heartbeat;
  - show stale nodes and last sync timestamp in diagnostics.

### 11) 3x-ui migration UX and installer target profile
- Rework subscription naming/export style to be closer to 3x-ui conventions:
  - use familiar labels/remarks and URI naming patterns that 3x-ui users expect;
  - align field names in CLI output where possible (without breaking protocol correctness);
  - minimize migration friction for users moving from 3x-ui panel.
- Add compatibility mode for 3x-ui-like subscription presentation:
  - optional toggle in config/wizard;
  - keep current mode available as default/fallback until validated.
- Installer profile selection:
  - during install ask what runtime stack user wants in final result: `xray only`, `sing-box only`, or `both`;
  - install/enable only selected runtime units and binaries by default;
  - avoid unnecessary services and reduce conflict surface on first boot.
- Wizard profile control:
  - add settings item to switch runtime profile post-install (`xray only` / `sing-box only` / `both`);
  - apply service enable/disable changes safely with confirmation and status report.

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
