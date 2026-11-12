-- Dream and study phases extend wake kinds without changing the earlier table's
-- semantics. SQLite cannot alter a CHECK constraint, so rebuild once and copy.
CREATE TABLE wakes_phase_migration (
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
INSERT INTO wakes_phase_migration(id,agent_id,started_at,ended_at,steps,tokens_in,tokens_out,cost_usd,end_reason,kind)
    SELECT id,agent_id,started_at,ended_at,steps,tokens_in,tokens_out,cost_usd,end_reason,kind FROM wakes;
DROP TABLE wakes;
ALTER TABLE wakes_phase_migration RENAME TO wakes;
CREATE UNIQUE INDEX wakes_phase_key ON wakes(agent_id,kind,phase_key) WHERE phase_key <> '';

-- Phase totals split dream spend from wake/study spend. `ledger` remains the
-- daily total for existing readers.
CREATE TABLE ledger_phases (
    agent_id TEXT NOT NULL,
    day TEXT NOT NULL,
    phase TEXT NOT NULL CHECK(phase IN ('agent','study','dream')),
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    PRIMARY KEY(agent_id,day,phase)
);
INSERT INTO ledger_phases(agent_id,day,phase,tokens_in,tokens_out,cost_usd)
    SELECT agent_id,day,'agent',tokens_in,tokens_out,cost_usd FROM ledger;

-- External trend stories observed by fetch_trends. `phase` separates study
-- observations (surfaced in the next wake) from agent-phase fetches used only
-- for deterministic spike detection. Titles and URLs never reach the dashboard.
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
CREATE INDEX trend_stories_recent ON trend_stories(agent_id,phase,observed_at);

-- Public Threads posts sampled read-only during study. Counters are nullable:
-- an absent visible counter stays NULL rather than becoming a fake zero. Text
-- is a bounded snippet plus a hash, never a stored profile of the author.
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
CREATE INDEX threads_observations_recent ON threads_observations(agent_id,observed_at);
CREATE INDEX threads_observations_post ON threads_observations(agent_id,post_url,observed_at);

