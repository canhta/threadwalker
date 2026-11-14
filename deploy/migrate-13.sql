-- Schema v12 -> v13. One-off; run against a STOPPED service, after backing up
-- the database:  sqlite3 -bail threadz.db < migrate-13.sql
-- -bail is required: it stops the script at the first error. The file refuses
-- to run on any other schema version and is one transaction: it applies
-- completely or not at all. It only adds a table; no row changes.
--
-- The store test (TestMigrate13MatchesFreshSchema) builds v12 from
-- internal/store/testdata/schema-v12.sql, applies this file and compares the
-- result with a fresh v13 database; keep the two in step.

BEGIN IMMEDIATE;

-- Abort unless this is a v12 database: a failing CHECK-style insert into a
-- temp table is the portable way to stop a sqlite3 script.
CREATE TEMP TABLE migrate_guard(ok INTEGER NOT NULL CHECK (ok = 1));
INSERT INTO migrate_guard SELECT (SELECT user_version FROM pragma_user_version) = 12;
DROP TABLE migrate_guard;

-- Durable post ids: every post a read returns keeps one id (shown to the
-- model as p<id>) per agent and URL. Rows are pruned 7 days after the read.
CREATE TABLE seen_posts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    url TEXT NOT NULL,
    media_id TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL,
    text TEXT NOT NULL,
    likes INTEGER,
    replies INTEGER,
    age_minutes INTEGER,
    read_at TEXT NOT NULL
);

CREATE UNIQUE INDEX seen_posts_url ON seen_posts(agent_id,url);

CREATE INDEX seen_posts_recent ON seen_posts(agent_id,read_at DESC);

-- Append further v13 changes above this line.

PRAGMA user_version = 13;

COMMIT;
