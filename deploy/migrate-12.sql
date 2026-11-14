-- Schema v11 -> v12. One-off; run against a STOPPED service, after backing up
-- the database:  sqlite3 -bail threadz.db < migrate-12.sql
-- Needs sqlite3 3.35.0 or later (ALTER TABLE ... DROP COLUMN). -bail is required:
-- it stops the script at the first error. The file refuses to run on any other
-- schema version and is one transaction: it applies completely or not at all.
--
-- The store test (TestMigrate12MatchesFreshSchema) builds v11 from
-- internal/store/testdata/schema-v11.sql, applies this file and compares the
-- result with a fresh v12 database; keep the two in step.

BEGIN IMMEDIATE;

-- Abort unless this is a v11 database: a failing CHECK-style insert into a
-- temp table is the portable way to stop a sqlite3 script.
CREATE TEMP TABLE migrate_guard(ok INTEGER NOT NULL CHECK (ok = 1));
INSERT INTO migrate_guard SELECT (SELECT user_version FROM pragma_user_version) = 11;
DROP TABLE migrate_guard;

-- 1. Learning, memory and notes stores are gone.
DROP TRIGGER IF EXISTS notes_ad;
DROP TRIGGER IF EXISTS notes_ai;
DROP TRIGGER IF EXISTS notes_au;
DROP TRIGGER IF EXISTS notes_embedding_ad;
DROP TRIGGER IF EXISTS notes_embedding_au;
DROP TRIGGER IF EXISTS actions_embedding_ad;
DROP TRIGGER IF EXISTS actions_embedding_au;
DROP TRIGGER IF EXISTS actions_ad;
DROP TRIGGER IF EXISTS actions_ai;
DROP TRIGGER IF EXISTS actions_au;
DROP TABLE IF EXISTS notes_fts;
DROP TABLE IF EXISTS actions_fts;
DROP TABLE IF EXISTS notes;
DROP TABLE IF EXISTS embeddings;
DROP TABLE IF EXISTS memory_index;
DROP TABLE IF EXISTS memory_journal;
DROP TABLE IF EXISTS strategy_revisions;
DROP TABLE IF EXISTS knowledge_candidates;
DROP TABLE IF EXISTS playbook;
DROP TABLE IF EXISTS playbook_decisions;
DROP TABLE IF EXISTS playbook_initializations;
DROP TABLE IF EXISTS playbook_outcomes;
DROP TABLE IF EXISTS action_hypotheses;
DROP TABLE IF EXISTS action_outcomes;
DROP TABLE IF EXISTS outcome_models;
DROP TABLE IF EXISTS threads_observations;

-- 2. Hypothesis tags, exploration and the posting-delay figure are gone from actions.
ALTER TABLE actions DROP COLUMN hypothesis_ids_json;
ALTER TABLE actions DROP COLUMN explore;
ALTER TABLE actions DROP COLUMN delay_minutes;

-- 3. The study phase is gone. Its wakes and their audit rows are deleted;
-- its spend is folded into the agent phase so every daily total stays whole.
DELETE FROM tool_call_details WHERE wake_id IN (SELECT id FROM wakes WHERE kind = 'study');
DELETE FROM tool_calls WHERE wake_id IN (SELECT id FROM wakes WHERE kind = 'study');
DELETE FROM wake_transcripts WHERE wake_id IN (SELECT id FROM wakes WHERE kind = 'study');
DELETE FROM wakes WHERE kind = 'study';
DELETE FROM trend_stories WHERE phase = 'study';

INSERT INTO ledger_phases(agent_id, day, phase, tokens_in, tokens_out, cost_usd)
SELECT agent_id, day, 'agent', tokens_in, tokens_out, cost_usd FROM ledger_phases WHERE phase = 'study'
ON CONFLICT(agent_id, day, phase) DO UPDATE SET
    tokens_in = ledger_phases.tokens_in + excluded.tokens_in,
    tokens_out = ledger_phases.tokens_out + excluded.tokens_out,
    cost_usd = ledger_phases.cost_usd + excluded.cost_usd;
DELETE FROM ledger_phases WHERE phase = 'study';

-- A CHECK cannot be altered: ledger_phases and wakes are rebuilt without 'study'.
CREATE TABLE ledger_phases_new (
    agent_id TEXT NOT NULL,
    day TEXT NOT NULL,
    phase TEXT NOT NULL CHECK(phase IN ('agent','dream','critic')),
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    PRIMARY KEY(agent_id,day,phase)
);
INSERT INTO ledger_phases_new SELECT agent_id, day, phase, tokens_in, tokens_out, cost_usd FROM ledger_phases;
DROP TABLE ledger_phases;
ALTER TABLE ledger_phases_new RENAME TO ledger_phases;

CREATE TABLE wakes_new (
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
INSERT INTO wakes_new SELECT id, agent_id, started_at, ended_at, steps, tokens_in, tokens_out, cost_usd, end_reason, kind, phase_key FROM wakes;
DROP TABLE wakes;
ALTER TABLE wakes_new RENAME TO wakes;
CREATE INDEX dashboard_wakes_started ON wakes(agent_id,started_at DESC);
CREATE UNIQUE INDEX wakes_phase_key ON wakes(agent_id,kind,phase_key) WHERE phase_key <> '';

-- trend_stories no longer has a phase: every story is an agent fetch.
DROP INDEX IF EXISTS trend_stories_recent;
ALTER TABLE trend_stories DROP COLUMN phase;

-- Append further v12 changes above this line.

PRAGMA user_version = 12;

COMMIT;
