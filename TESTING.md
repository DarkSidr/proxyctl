# proxyctl Manual Smoke Checklist

Этот checklist проверяет основной MVP flow на single-node Linux хосте.

## Preconditions
- Debian 12 / Ubuntu 22.04 / Ubuntu 24.04.
- Права `sudo`.
- Установлены `systemd`, `journalctl`, `sqlite3`.

## Smoke Flow
1. `install`
- [ ] Выполнить установку:
```bash
sudo bash install.sh
```
- [ ] Проверить бинарник:
```bash
proxyctl --help
```

2. `init`
- [ ] Инициализировать БД:
```bash
sudo proxyctl init
```

3. `add user`
- [ ] Создать пользователя:
```bash
sudo proxyctl user add --name alice
```
- [ ] Проверить список:
```bash
sudo proxyctl user list
```

4. Подготовка node (нужно для inbound)
- [ ] Создать primary node:
```bash
sudo proxyctl node add --name main --host 127.0.0.1 --role primary
```
- [ ] Скопировать `id` из `proxyctl node list` как `<NODE_ID>`.

5. `add inbound vless`
- [ ] Добавить inbound:
```bash
sudo proxyctl inbound add --type vless --transport ws --node-id <NODE_ID> --domain example.com --port 443 --tls --path /vless
```

6. `add inbound hysteria2`
- [ ] Добавить inbound:
```bash
sudo proxyctl inbound add --type hysteria2 --transport udp --node-id <NODE_ID> --domain example.com --port 8443 --tls
```

7. `add inbound xhttp`
- [ ] Добавить inbound:
```bash
sudo proxyctl inbound add --type xhttp --transport xhttp --node-id <NODE_ID> --domain example.com --port 9443 --tls --path /xhttp
```

8. Привязка credentials (для подписки)
- [ ] Получить IDs:
```bash
sudo proxyctl user list
sudo proxyctl inbound list
```
- [ ] Для каждого нужного inbound добавить credential напрямую в SQLite (MVP: отдельной CLI-команды пока нет):
```bash
sudo sqlite3 /var/lib/proxy-orchestrator/proxyctl.db \
"INSERT INTO credentials (id, user_id, inbound_id, kind, secret, metadata, created_at) VALUES (hex(randomblob(16)), '<USER_ID>', '<INBOUND_ID>', 'uuid', lower(hex(randomblob(16))), '{}', strftime('%Y-%m-%dT%H:%M:%fZ','now'));"
```

9. `generate subscription`
- [ ] Сгенерировать подписку:
```bash
sudo proxyctl subscription generate alice
```
- [ ] Проверить вывод:
```bash
sudo proxyctl subscription show alice
```

10. `validate`
- [ ] Запустить предвалидацию:
```bash
sudo proxyctl validate
```

11. `apply`
- [ ] Применить runtime-конфиг:
```bash
sudo proxyctl apply
```

12. `status`
- [ ] Проверить состояние:
```bash
sudo proxyctl status
```

13. `logs`
- [ ] Проверить runtime-логи:
```bash
sudo proxyctl logs --lines 100
```
- [ ] Проверить apply-логи (если unit используется):
```bash
sudo proxyctl logs apply --lines 100
```

## Local Dev Checks
- [ ] Запустить инженерный чек:
```bash
make check
```
- [ ] Проверить CLI smoke:
```bash
make smoke-help
```
