-- Schema v12 -> v13. One-off; run against a STOPPED service, after backing up
-- the database:  sqlite3 -bail threadz.db < migrate-13.sql
-- -bail is required: it stops the script at the first error. The file refuses
-- to run on any other schema version and is one transaction: it applies
-- completely or not at all. It adds a table, drops actions.topic_heat (never
-- read), renames actions.trend_ref to actions.trend and rewrites the host of
-- stored Threads URLs to threads.com; no other row changes. DROP COLUMN needs
-- sqlite3 3.35 or later and UPDATE ... FROM 3.33 or later (check: sqlite3 --version).
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

-- actions.topic_heat was written and never read.
ALTER TABLE actions DROP COLUMN topic_heat;

-- "ref" now means the p and t ids; the stored anchor is the trend itself.
ALTER TABLE actions RENAME COLUMN trend_ref TO trend;

-- One post has one address: CanonicalThread now maps threads.net and www. hosts
-- to threads.com, and replies_per_conversation matches root_url in SQL. Every
-- stored Threads URL column gets the same host. Only the host changes; the
-- character after it must end the host, so graph.threads.net or threads.net.x
-- never match.
CREATE TEMP TABLE old_host(prefix TEXT PRIMARY KEY);
INSERT INTO old_host VALUES
    ('https://threads.net'), ('https://www.threads.net'), ('https://www.threads.com'),
    ('http://threads.net'), ('http://www.threads.net'), ('http://threads.com'), ('http://www.threads.com');

UPDATE actions SET target_url = 'https://threads.com' || substr(target_url, length(prefix) + 1)
    FROM old_host WHERE substr(target_url, 1, length(prefix)) = prefix AND substr(target_url, length(prefix) + 1, 1) IN ('', '/', '?', '#');
UPDATE actions SET thread_url = 'https://threads.com' || substr(thread_url, length(prefix) + 1)
    FROM old_host WHERE substr(thread_url, 1, length(prefix)) = prefix AND substr(thread_url, length(prefix) + 1, 1) IN ('', '/', '?', '#');
UPDATE actions SET root_url = 'https://threads.com' || substr(root_url, length(prefix) + 1)
    FROM old_host WHERE substr(root_url, 1, length(prefix)) = prefix AND substr(root_url, length(prefix) + 1, 1) IN ('', '/', '?', '#');
UPDATE action_parts SET thread_url = 'https://threads.com' || substr(thread_url, length(prefix) + 1)
    FROM old_host WHERE substr(thread_url, 1, length(prefix)) = prefix AND substr(thread_url, length(prefix) + 1, 1) IN ('', '/', '?', '#');

DROP TABLE old_host;

-- Append further v13 changes above this line.

PRAGMA user_version = 13;

COMMIT;
