# Агент на роутере (`agent/`)

## Почему ucode

На роутере только busybox, без python и jq. ucode уже стоит: на нём написан forkop. В ucode есть JSON, `fs` и запуск процессов, поэтому разбор clash API и отчёты пишутся нормально, без sed по JSON. HTTP через `curl` (он есть на роутерах пользователя).

## Файлы на роутере

```
/usr/bin/listok-agent            # ucode-скрипт
/etc/init.d/listok-agent         # procd, respawn
/etc/config/listok               # uci: server, agent_token, секции и токены фидов, report_*
/etc/listok/<section>.lst        # последний полученный фид (флеш: overlay, переживает ребут)
/etc/listok/<section>.etag
```

## Цикл синхронизации (по одному на секцию)

1. `curl -H 'If-None-Match: <etag>' '<server>/f/<token>.lst?wait=55'`
2. `304` → сразу следующий запрос. `200` → записать во временный файл, проверить (не пустой, все строки похожи на домен или CIDR), затем `mv` поверх `/etc/listok/<section>.lst` и сохранить etag.
3. Вызвать `forkop list_update`. Если идёт другой `list_update` (у forkop есть pid-lock), повторить через 10 с.
4. Отправить `POST /agent/v1/applied` с результатом.
5. Ошибка сети → пауза с ростом от 5 с до 5 мин и повтор. Файл не трогается.

Пишем на флеш только при реальном изменении.

## Отчёты из clash API

- Раз в 30 с: `GET http://127.0.0.1:9090/connections` (с `Authorization: Bearer <secret>`, если задан `yacd_secret_key`).
- Берутся соединения, у которых в `chains` есть `direct-out`, а `metadata.host` не пустой и не IP. Копятся в памяти: host → hits, bytes, пример IP/порта/сети, rule.
- Раз в `report_interval_s` (по умолчанию 300) уходит на `/agent/v1/report`, накопленное очищается.
- Локальный фильтр не отправляет домены LAN и `*.lan`/`*.local`, а также то, что уже есть в собственных фидах.
- Выключается через `report_enabled` (на сервере для роутера и в uci локально). Для роутера товарища по умолчанию **выключено**.

На сервере отчёты попадают в `observations`. Они проходят через `suggestion_ignores` и проверку покрытия. В интерфейсе показываются хосты со статусом `new`, сгруппированные по eTLD+1: «добавить в список…», «скрыть», «игнорировать шаблон».

## Установка

`curl -fsSL https://listok.<домен>/install/<token> | sh`. Установщик:
1. Кладёт агент, init-скрипт и `/etc/config/listok` (токен агента и токены фидов уже подставлены сервером).
2. Делает первую синхронизацию, чтобы файлы `/etc/listok/*.lst` существовали.
3. Добавляет в forkop локальный путь: `uci add_list forkop.<section>.domain_ip_lists='/etc/listok/<section>.lst'`, `uci commit forkop`. **Это вызовет один перезапуск forkop**, единственный за всё время.
4. Включает и запускает `listok-agent`.
5. Перед изменениями сохраняет бэкап `/etc/config/forkop.bak-listok-<дата>`.

Удаление: `listok-agent uninstall` убирает путь из uci, сервис и файлы.

Самообновление: `hello` возвращает актуальную версию. Если она новее, агент скачивает `/agent/listok-agent.uc`, проверяет sha256 и перезапускает себя.
