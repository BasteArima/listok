-- Режим агента (docs/router-agent.md): 'wait' — long-poll, 'poll' — проверка раз в agent_interval_s.
-- Сообщается агентом в hello, показывается на карточке роутера.

ALTER TABLE routers ADD COLUMN agent_mode       TEXT;
ALTER TABLE routers ADD COLUMN agent_interval_s INTEGER;
