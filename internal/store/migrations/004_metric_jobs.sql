ALTER TABLE wakes ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent' CHECK(kind IN ('agent','metrics'));

CREATE TABLE metric_jobs (
    agent_id TEXT NOT NULL,
    action_id INTEGER NOT NULL,
    window_hours INTEGER NOT NULL CHECK(window_hours IN (1,6,24)),
    due_at TEXT NOT NULL,
    not_before TEXT NOT NULL,
    claim_version INTEGER NOT NULL DEFAULT 0 CHECK(claim_version >= 0),
    claimed_at TEXT,
    lease_until TEXT,
    completed_at TEXT,
    reason TEXT NOT NULL DEFAULT '' CHECK(reason IN ('','metrics_unavailable','source_unavailable','read_failed','binding_mismatch','auth_required','cancelled','persistence_failed','measurement_failed')),
    PRIMARY KEY(agent_id,action_id,window_hours),
    CHECK((claimed_at IS NULL) = (lease_until IS NULL)),
    CHECK(lease_until IS NULL OR lease_until > claimed_at),
    CHECK(completed_at IS NULL OR lease_until IS NULL)
);
CREATE INDEX metric_jobs_pending ON metric_jobs(agent_id,completed_at,not_before,due_at);
