CREATE TABLE account_followers (
    agent_id TEXT PRIMARY KEY,
    followers INTEGER NOT NULL,
    at TEXT NOT NULL
);

CREATE TABLE actions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('post', 'reply')),
    text TEXT NOT NULL,
    target_url TEXT NOT NULL DEFAULT '',
    target_username TEXT NOT NULL DEFAULT '',
    dry_run INTEGER NOT NULL DEFAULT 1,
    posted_at TEXT NOT NULL,
    thread_url TEXT NOT NULL DEFAULT '',
    target_score_json TEXT NOT NULL DEFAULT '',
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
    critic_json TEXT NOT NULL DEFAULT '',
    root_url TEXT NOT NULL DEFAULT '',
    angle TEXT NOT NULL DEFAULT '',
    fact TEXT NOT NULL DEFAULT '');

CREATE TABLE action_lessons (
    action_id INTEGER NOT NULL REFERENCES actions(id),
    lesson_id INTEGER NOT NULL REFERENCES lessons(id),
    PRIMARY KEY (action_id, lesson_id)
) WITHOUT ROWID;

CREATE TABLE action_parts (
    agent_id TEXT NOT NULL,
    action_id INTEGER NOT NULL REFERENCES actions(id),
    part INTEGER NOT NULL CHECK (part >= 2),
    text TEXT NOT NULL,
    thread_url TEXT NOT NULL DEFAULT '',
    media_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (action_id, part)
) WITHOUT ROWID;

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

CREATE TABLE ledger_phases (
    agent_id TEXT NOT NULL,
    day TEXT NOT NULL,
    phase TEXT NOT NULL CHECK(phase IN ('agent','dream','critic')),
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    tokens_cached INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(agent_id,day,phase)
);

CREATE TABLE lessons (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('post', 'reply', 'any')),
    polarity TEXT NOT NULL CHECK (polarity IN ('do', 'avoid')),
    text TEXT NOT NULL,
    evidence_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    confirmed_at TEXT NOT NULL,
    retired_at TEXT,
    retired_by TEXT CHECK (retired_by IN ('dream', 'owner', 'expiry')),
    retire_reason TEXT NOT NULL DEFAULT '',
    CHECK ((retired_at IS NULL) = (retired_by IS NULL))
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

CREATE TABLE wakes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    steps INTEGER NOT NULL DEFAULT 0,
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    end_reason TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'agent' CHECK(kind IN ('agent','metrics','dream')),
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

CREATE INDEX lessons_agent ON lessons(agent_id,retired_at);

CREATE INDEX action_lessons_lesson ON action_lessons(lesson_id);

CREATE INDEX dashboard_metrics_agent ON metrics(agent_id,measured_at);

CREATE INDEX dashboard_tool_calls_at ON tool_calls(agent_id,at DESC);

CREATE INDEX tool_call_details_wake ON tool_call_details(agent_id,wake_id);

CREATE INDEX dashboard_trends_recent ON trend_stories(agent_id,observed_at DESC);

CREATE INDEX dashboard_wakes_started ON wakes(agent_id,started_at DESC);

CREATE INDEX metric_jobs_pending ON metric_jobs(agent_id,completed_at,not_before,due_at);

CREATE UNIQUE INDEX owner_event ON owner_msgs(agent_id,event_key);

CREATE INDEX owner_outbox ON owner_msgs(agent_id,direction,delivery);

CREATE INDEX wake_transcripts_agent ON wake_transcripts(agent_id, wake_id);

CREATE UNIQUE INDEX wake_transcripts_seq ON wake_transcripts(wake_id, seq);

CREATE UNIQUE INDEX wakes_phase_key ON wakes(agent_id,kind,phase_key) WHERE phase_key <> '';
