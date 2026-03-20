# TODO / Feature Ideas

## Panel UI — Advanced Inbound Options (inspired by 3x-ui)

- **Fallbacks** — per-inbound fallback server list (path/dest/xver per entry); useful for
  routing TCP connections to different backends based on SNI/path
- **Vision Seed** — 4 numeric seed values for XTLS Vision padding optimisation + Rand/Reset buttons
- **Proxy Protocol** — toggle `acceptProxyProtocol` on the inbound sockopt (get real client IP
  behind nginx/HAProxy)
- **HTTP Masquerade** — HTTP obfuscation headers for TCP transport
- **Sockopt** — expose raw socket options: TProxy mode, TCP Fast Open, socket mark, etc.
- **UDP Masks** — experimental xray UDP masking entries
- **External Proxy** — outbound chain (proxy-behind-proxy)
- **Show / Xver** — `show` flag and `xver` (Proxy Protocol version) in Reality settings
- **mldsa65 Seed / Verify** — post-quantum Reality key material fields

## Panel UI — Other

- **Inbound comments/notes** — free-text annotation per inbound
- **Traffic reset schedule** — per-inbound traffic counter reset
- **Expiry date** — per-credential expiry timestamp
- **Client traffic stats** — per-user bandwidth display on the dashboard
- **Dark/light theme toggle**

## Backend

- **sing-box sniffing** — mirror SniffingEnabled fields to sing-box renderer
  (currently only wired to xray renderer)
- **Automated Reality key rotation** — background job to regenerate Reality keys
- **Multi-port inbounds** — one config entry covering a port range
