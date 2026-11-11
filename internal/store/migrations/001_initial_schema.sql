CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('running', 'paused', 'killed')),
    next_wake_at TEXT,
    self_note TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS wakes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    steps INTEGER NOT NULL DEFAULT 0,
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    end_reason TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS tool_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL DEFAULT '',
    wake_id INTEGER NOT NULL,
    tool TEXT NOT NULL,
    args_json TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '',
    blocked_by TEXT NOT NULL DEFAULT '',
    at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS actions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('post', 'reply')),
    text TEXT NOT NULL,
    target_url TEXT NOT NULL DEFAULT '',
    target_username TEXT NOT NULL DEFAULT '',
    hypothesis_ids_json TEXT NOT NULL DEFAULT '[]',
    dry_run INTEGER NOT NULL DEFAULT 1,
    posted_at TEXT NOT NULL,
    thread_url TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS metrics (
    agent_id TEXT NOT NULL DEFAULT '',
    action_id INTEGER NOT NULL,
    measured_at TEXT NOT NULL,
    views INTEGER NOT NULL DEFAULT 0,
    likes INTEGER NOT NULL DEFAULT 0,
    replies INTEGER NOT NULL DEFAULT 0,
    reposts INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (action_id, measured_at)
);

CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
    agent_id UNINDEXED,
    text,
    content='notes',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS notes_ai AFTER INSERT ON notes BEGIN
    INSERT INTO notes_fts(rowid, agent_id, text) VALUES (new.id, new.agent_id, new.text);
END;

CREATE TRIGGER IF NOT EXISTS notes_ad AFTER DELETE ON notes BEGIN
    INSERT INTO notes_fts(notes_fts, rowid, agent_id, text) VALUES ('delete', old.id, old.agent_id, old.text);
END;

CREATE TRIGGER IF NOT EXISTS notes_au AFTER UPDATE ON notes BEGIN
    INSERT INTO notes_fts(notes_fts, rowid, agent_id, text) VALUES ('delete', old.id, old.agent_id, old.text);
    INSERT INTO notes_fts(rowid, agent_id, text) VALUES (new.id, new.agent_id, new.text);
END;

CREATE TABLE IF NOT EXISTS playbook (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    text TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('testing', 'established', 'rejected')),
    n INTEGER NOT NULL DEFAULT 0,
    successes INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    decided_at TEXT
);

CREATE TABLE IF NOT EXISTS contacts (
    agent_id TEXT NOT NULL,
    username TEXT NOT NULL,
    reply_count_7d INTEGER NOT NULL DEFAULT 0,
    do_not_contact INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (agent_id, username)
);

CREATE TABLE IF NOT EXISTS ledger (
    agent_id TEXT NOT NULL,
    day TEXT NOT NULL,
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (agent_id, day)
);

CREATE TABLE IF NOT EXISTS owner_msgs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    direction TEXT NOT NULL,
    text TEXT NOT NULL,
    answered_id INTEGER,
    at TEXT NOT NULL
);
