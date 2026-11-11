-- Revision history for code-owned hypothesis counter resets. Initial rows are
-- revision 1; amending the text of a testing entry increments the revision and
-- resets the evidence counters so changed text cannot inherit old samples.
ALTER TABLE playbook ADD COLUMN revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1);

-- Once-per-agent seed marker. It is independent of the playbook rows so a
-- restart, a changed seed template, or manually removed rows can never
-- reinsert hypotheses or overwrite runtime state.
CREATE TABLE playbook_initializations (
    agent_id TEXT PRIMARY KEY,
    initialized_at TEXT NOT NULL
);
