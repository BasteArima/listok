-- Секции forkop на роутере (docs/router-agent.md): агент с 0.4.0 присылает их в hello,
-- экран роутера показывает по карточке на секцию. JSON-массив [{"name": "...", "action": "..."}];
-- NULL — агент ещё не присылал (агент 0.3.x или роутер без агента).

ALTER TABLE routers ADD COLUMN forkop_sections TEXT;
