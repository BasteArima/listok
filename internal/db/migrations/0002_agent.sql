-- Агент на роутере (этап 2): результат применения фида через forkop list_update.
-- Описание: docs/db-schema.md, docs/router-agent.md.

ALTER TABLE feeds ADD COLUMN applied_etag  TEXT;     -- версия, которую агент применил последней
ALTER TABLE feeds ADD COLUMN applied_at    INTEGER;  -- когда пришёл отчёт
ALTER TABLE feeds ADD COLUMN applied_ok    INTEGER;  -- 1 — rule-set пересобран, 0 — ошибка
ALTER TABLE feeds ADD COLUMN applied_error TEXT;     -- текст ошибки от агента (до 300 символов)
