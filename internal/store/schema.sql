CREATE TABLE account_followers (
    agent_id TEXT PRIMARY KEY,
    followers INTEGER NOT NULL,
    at TEXT NOT NULL
);

CREATE TABLE action_hypotheses (
 agent_id TEXT NOT NULL, action_id INTEGER NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 PRIMARY KEY(agent_id,action_id,hypothesis_id)
);

CREATE TABLE actions (
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
, explore INTEGER NOT NULL DEFAULT 0 CHECK (explore IN (0, 1)), target_score_json TEXT NOT NULL DEFAULT '', delay_minutes INTEGER,
    media_id TEXT NOT NULL DEFAULT '',
    topic_heat REAL CHECK (topic_heat IS NULL OR (topic_heat >= 0 AND topic_heat <= 1)),
    followers_at_post INTEGER CHECK (followers_at_post IS NULL OR followers_at_post >= 0),
    parent_replies INTEGER CHECK (parent_replies IS NULL OR parent_replies >= 0),
    parent_age_minutes INTEGER CHECK (parent_age_minutes IS NULL OR parent_age_minutes >= 0),
    hook TEXT NOT NULL DEFAULT '',
    rationale TEXT NOT NULL DEFAULT '',
    failed INTEGER NOT NULL DEFAULT 0 CHECK (failed IN (0, 1)),
    browser INTEGER NOT NULL DEFAULT 0 CHECK (browser IN (0, 1)),
    trend_ref TEXT NOT NULL DEFAULT '',
    answers_note INTEGER NOT NULL DEFAULT 0,
    critic_json TEXT NOT NULL DEFAULT '');

CREATE TABLE action_parts (
    agent_id TEXT NOT NULL,
    action_id INTEGER NOT NULL REFERENCES actions(id),
    part INTEGER NOT NULL CHECK (part >= 2),
    text TEXT NOT NULL,
    thread_url TEXT NOT NULL DEFAULT '',
    media_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (action_id, part)
) WITHOUT ROWID;

CREATE VIRTUAL TABLE actions_fts USING fts5(
    agent_id UNINDEXED,
    text,
    content='actions',
    content_rowid='id',
    tokenize='unicode61 remove_diacritics 0'
);

CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('running', 'paused', 'killed')),
    next_wake_at TEXT);

CREATE TABLE contacts (
    agent_id TEXT NOT NULL,
    username TEXT NOT NULL,
    reply_count_7d INTEGER NOT NULL DEFAULT 0,
    do_not_contact INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (agent_id, username)
);

CREATE TABLE embeddings (
    source TEXT NOT NULL CHECK (source IN ('note', 'action')),
    source_id INTEGER NOT NULL,
    text_hash TEXT NOT NULL,
    vector BLOB NOT NULL,
    PRIMARY KEY (source, source_id)
) WITHOUT ROWID;

CREATE TABLE knowledge_candidates (
    id INTEGER PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id),
    technique TEXT NOT NULL,
    source_url TEXT NOT NULL,
    evidence TEXT NOT NULL DEFAULT 'practitioner' CHECK (evidence = 'practitioner'),
    created_at TEXT NOT NULL,
    UNIQUE (agent_id, source_url)
);

CREATE TABLE ledger_phases (
    agent_id TEXT NOT NULL,
    day TEXT NOT NULL,
    phase TEXT NOT NULL CHECK(phase IN ('agent','study','dream','critic')),
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    PRIMARY KEY(agent_id,day,phase)
);

CREATE TABLE memory_index (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at TEXT NOT NULL,
    archived_at TEXT
);

CREATE TABLE memory_journal (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    entry_id INTEGER NOT NULL,
    change TEXT NOT NULL,
    old_text TEXT NOT NULL DEFAULT '',
    new_text TEXT NOT NULL DEFAULT '',
    wake_id INTEGER NOT NULL DEFAULT 0,
    phase TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE TABLE metric_jobs (
    agent_id TEXT NOT NULL,
    action_id INTEGER NOT NULL,
    window_hours INTEGER NOT NULL CHECK(window_hours IN (1,6,24)),
    due_at TEXT NOT NULL,
    not_before TEXT NOT NULL,
    claim_version INTEGER NOT NULL DEFAULT 0 CHECK(claim_version >= 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    claimed_at TEXT,
    lease_until TEXT,
    completed_at TEXT,
    reason TEXT NOT NULL DEFAULT '' CHECK(reason IN ('','metrics_unavailable','source_unavailable','read_failed','binding_mismatch','auth_required','cancelled','persistence_failed','measurement_failed','media_gone')),
    PRIMARY KEY(agent_id,action_id,window_hours),
    CHECK((claimed_at IS NULL) = (lease_until IS NULL)),
    CHECK(lease_until IS NULL OR lease_until > claimed_at),
    CHECK(completed_at IS NULL OR lease_until IS NULL)
);

CREATE TABLE metrics (
    agent_id TEXT NOT NULL DEFAULT '',
    action_id INTEGER NOT NULL,
    measured_at TEXT NOT NULL,
    views INTEGER NOT NULL DEFAULT 0,
    likes INTEGER NOT NULL DEFAULT 0,
    replies INTEGER NOT NULL DEFAULT 0,
    reposts INTEGER NOT NULL DEFAULT 0,
    quotes INTEGER CHECK (quotes IS NULL OR quotes >= 0),
    shares INTEGER CHECK (shares IS NULL OR shares >= 0),
    followers INTEGER CHECK (followers IS NULL OR followers >= 0),
    author_replied INTEGER CHECK (author_replied IS NULL OR author_replied IN (0, 1)),
    PRIMARY KEY (action_id, measured_at)
);

CREATE TABLE outcome_models (
    agent_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    fitted_at TEXT NOT NULL,
    rows INTEGER NOT NULL CHECK (rows >= 0),
    coefficients_json TEXT NOT NULL,
    PRIMARY KEY (agent_id, version)
);

CREATE TABLE action_outcomes (
    agent_id TEXT NOT NULL,
    action_id INTEGER NOT NULL,
    measured_at TEXT NOT NULL,
    model_version INTEGER NOT NULL CHECK (model_version >= 0),
    views INTEGER NOT NULL CHECK (views > 0),
    conversation_rate REAL NOT NULL,
    amplification_rate REAL,
    applause_rate REAL NOT NULL,
    followers_per_1k REAL,
    reach_lift REAL NOT NULL,
    resonance_lift REAL NOT NULL,
    applause_lift REAL,
    label TEXT NOT NULL CHECK (label IN ('viral', 'flop', 'ordinary')),
    PRIMARY KEY (agent_id, action_id)
);

CREATE TABLE notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at TEXT NOT NULL
, topic TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '');

CREATE VIRTUAL TABLE notes_fts USING fts5(
    agent_id UNINDEXED,
    text,
    content='notes',
    content_rowid='id',
    tokenize='unicode61 remove_diacritics 0'
);

CREATE TABLE owner_msgs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    direction TEXT NOT NULL,
    text TEXT NOT NULL,
    answered_id INTEGER,
    at TEXT NOT NULL
, kind TEXT NOT NULL DEFAULT 'notification', event_key TEXT, delivery TEXT NOT NULL DEFAULT 'queued', message_id INTEGER, bot_key TEXT NOT NULL DEFAULT '');

CREATE TABLE owner_poll (
    agent_id TEXT NOT NULL,
    bot_key TEXT NOT NULL,
    next_update INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(agent_id,bot_key)
);

CREATE TABLE playbook (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    text TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('testing', 'established', 'rejected')),
    n INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    decided_at TEXT
, revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1), knowledge_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '' CHECK (kind IN ('', 'post', 'reply')));

CREATE TABLE playbook_decisions (
 agent_id TEXT NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('established','rejected')), n INTEGER NOT NULL,
 reason TEXT NOT NULL, decided_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,hypothesis_id,revision)
);

CREATE TABLE playbook_initializations (
    agent_id TEXT PRIMARY KEY,
    initialized_at TEXT NOT NULL
);

CREATE TABLE playbook_outcomes (
 agent_id TEXT NOT NULL, action_id INTEGER NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 measured_at TEXT NOT NULL,
 reason TEXT NOT NULL, counted INTEGER NOT NULL CHECK(counted IN (0,1)),
 PRIMARY KEY(agent_id,action_id,hypothesis_id)
);

-- A read tool the Meta app may not use, hidden from the agent for 24 hours.
CREATE TABLE refused_tools (
    agent_id TEXT NOT NULL,
    tool TEXT NOT NULL,
    at TEXT NOT NULL,
    PRIMARY KEY(agent_id,tool)
);

CREATE TABLE secrets (
    agent_id TEXT NOT NULL,
    name TEXT NOT NULL,
    sealed BLOB NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (agent_id, name)
);

CREATE TABLE strategy_revisions (
    agent_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version >= 1),
    text TEXT NOT NULL,
    citations_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (agent_id, version)
);

CREATE TABLE threads_observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    post_url TEXT NOT NULL,
    author TEXT NOT NULL,
    text_hash TEXT NOT NULL,
    snippet TEXT NOT NULL,
    replies INTEGER,
    likes INTEGER,
    reposts INTEGER,
    kind TEXT NOT NULL CHECK(kind IN ('feed','search')),
    keyword TEXT NOT NULL DEFAULT '',
    observed_at TEXT NOT NULL
);

CREATE TABLE tool_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL DEFAULT '',
    wake_id INTEGER NOT NULL,
    tool TEXT NOT NULL,
    args_json TEXT NOT NULL,
    result_json TEXT NOT NULL DEFAULT '',
    blocked_by TEXT NOT NULL DEFAULT '',
    at TEXT NOT NULL
);

CREATE TABLE tool_call_details (
    tool_call_id INTEGER PRIMARY KEY,
    agent_id TEXT NOT NULL,
    wake_id INTEGER NOT NULL,
    args_json TEXT NOT NULL,
    detail_json TEXT NOT NULL DEFAULT '',
    at TEXT NOT NULL
);

CREATE TABLE trend_stories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    phase TEXT NOT NULL CHECK(phase IN ('agent','study')),
    source TEXT NOT NULL,
    title TEXT NOT NULL,
    url TEXT NOT NULL,
    rank INTEGER NOT NULL DEFAULT 0,
    score INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE wake_transcripts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    wake_id INTEGER NOT NULL,
    seq INTEGER NOT NULL,
    kind TEXT NOT NULL,
    body TEXT NOT NULL,
    at TEXT NOT NULL
);

CREATE TABLE "wakes" (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    steps INTEGER NOT NULL DEFAULT 0,
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    end_reason TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'agent' CHECK(kind IN ('agent','metrics','dream','study')),
    phase_key TEXT NOT NULL DEFAULT ''
);

-- One row per Threads webhook event the agent was woken for: the field, the
-- object id and the time. Never the text or the author.
CREATE TABLE webhook_events (
    agent_id TEXT NOT NULL,
    field TEXT NOT NULL,
    object_id TEXT NOT NULL,
    at TEXT NOT NULL,
    PRIMARY KEY (agent_id, field, object_id)
);

CREATE INDEX dashboard_actions_own ON actions(agent_id,dry_run,id DESC);

CREATE INDEX dashboard_metrics_agent ON metrics(agent_id,measured_at);

CREATE INDEX dashboard_notes_agent ON notes(agent_id,id DESC);

CREATE INDEX dashboard_playbook_agent ON playbook(agent_id,id DESC);

CREATE INDEX dashboard_tool_calls_at ON tool_calls(agent_id,at DESC);

CREATE INDEX tool_call_details_wake ON tool_call_details(agent_id,wake_id);

CREATE INDEX dashboard_trends_recent ON trend_stories(agent_id,observed_at DESC);

CREATE INDEX dashboard_wakes_started ON wakes(agent_id,started_at DESC);

CREATE INDEX embeddings_text_hash ON embeddings(text_hash);

CREATE INDEX memory_index_live ON memory_index(agent_id, id) WHERE archived_at IS NULL;

CREATE INDEX memory_journal_agent ON memory_journal(agent_id, id);

CREATE INDEX metric_jobs_pending ON metric_jobs(agent_id,completed_at,not_before,due_at);

CREATE UNIQUE INDEX notes_topic ON notes(agent_id, topic) WHERE topic <> '';

CREATE UNIQUE INDEX owner_event ON owner_msgs(agent_id,event_key);

CREATE INDEX owner_outbox ON owner_msgs(agent_id,direction,delivery);

CREATE INDEX threads_observations_post ON threads_observations(agent_id,post_url,observed_at);

CREATE INDEX threads_observations_recent ON threads_observations(agent_id,observed_at);

CREATE INDEX trend_stories_recent ON trend_stories(agent_id,phase,observed_at);

CREATE INDEX wake_transcripts_agent ON wake_transcripts(agent_id, wake_id);

CREATE UNIQUE INDEX wake_transcripts_seq ON wake_transcripts(wake_id, seq);

CREATE UNIQUE INDEX wakes_phase_key ON wakes(agent_id,kind,phase_key) WHERE phase_key <> '';

CREATE TRIGGER actions_ad AFTER DELETE ON actions BEGIN
    INSERT INTO actions_fts(actions_fts, rowid, agent_id, text) VALUES ('delete', old.id, old.agent_id, old.text);
END;

CREATE TRIGGER actions_ai AFTER INSERT ON actions BEGIN
    INSERT INTO actions_fts(rowid, agent_id, text) VALUES (new.id, new.agent_id, new.text);
END;

CREATE TRIGGER actions_au AFTER UPDATE ON actions BEGIN
    INSERT INTO actions_fts(actions_fts, rowid, agent_id, text) VALUES ('delete', old.id, old.agent_id, old.text);
    INSERT INTO actions_fts(rowid, agent_id, text) VALUES (new.id, new.agent_id, new.text);
END;

CREATE TRIGGER actions_embedding_ad AFTER DELETE ON actions BEGIN
    DELETE FROM embeddings WHERE source = 'action' AND source_id = old.id;
END;

CREATE TRIGGER actions_embedding_au AFTER UPDATE OF text ON actions WHEN old.text <> new.text BEGIN
    DELETE FROM embeddings WHERE source = 'action' AND source_id = old.id;
END;

CREATE TRIGGER notes_ad AFTER DELETE ON notes BEGIN
    INSERT INTO notes_fts(notes_fts, rowid, agent_id, text) VALUES ('delete', old.id, old.agent_id, old.text);
END;

CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN
    INSERT INTO notes_fts(rowid, agent_id, text) VALUES (new.id, new.agent_id, new.text);
END;

CREATE TRIGGER notes_au AFTER UPDATE ON notes BEGIN
    INSERT INTO notes_fts(notes_fts, rowid, agent_id, text) VALUES ('delete', old.id, old.agent_id, old.text);
    INSERT INTO notes_fts(rowid, agent_id, text) VALUES (new.id, new.agent_id, new.text);
END;

CREATE TRIGGER notes_embedding_ad AFTER DELETE ON notes BEGIN
    DELETE FROM embeddings WHERE source = 'note' AND source_id = old.id;
END;

CREATE TRIGGER notes_embedding_au AFTER UPDATE OF text ON notes WHEN old.text <> new.text BEGIN
    DELETE FROM embeddings WHERE source = 'note' AND source_id = old.id;
END;
