ALTER TABLE owner_msgs ADD COLUMN kind TEXT NOT NULL DEFAULT 'notification';
ALTER TABLE owner_msgs ADD COLUMN event_key TEXT;
ALTER TABLE owner_msgs ADD COLUMN delivery TEXT NOT NULL DEFAULT 'queued';
ALTER TABLE owner_msgs ADD COLUMN message_id INTEGER;
CREATE UNIQUE INDEX owner_event ON owner_msgs(agent_id,event_key);
CREATE INDEX owner_outbox ON owner_msgs(agent_id,direction,delivery);
CREATE TABLE owner_poll (
    agent_id TEXT NOT NULL,
    bot_key TEXT NOT NULL,
    next_update INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(agent_id,bot_key)
);
