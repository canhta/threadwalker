-- Lifetime ordinals survive text edits and process restarts. No ordinal is reused.
CREATE TABLE playbook_tests (
 agent_id TEXT NOT NULL,
 hypothesis_id INTEGER NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 test_index INTEGER NOT NULL CHECK(test_index>0),
 PRIMARY KEY(agent_id,hypothesis_id,revision), UNIQUE(agent_id,test_index)
);
INSERT INTO playbook_tests(agent_id,hypothesis_id,revision,test_index)
 SELECT agent_id,id,revision,ROW_NUMBER() OVER(PARTITION BY agent_id ORDER BY id) FROM playbook;
CREATE TRIGGER playbook_test_added AFTER INSERT ON playbook BEGIN
 INSERT INTO playbook_tests(agent_id,hypothesis_id,revision,test_index)
 VALUES(new.agent_id,new.id,new.revision,COALESCE((SELECT MAX(test_index) FROM playbook_tests WHERE agent_id=new.agent_id),0)+1);
END;
CREATE TRIGGER playbook_test_amended AFTER UPDATE OF revision ON playbook WHEN new.revision<>old.revision BEGIN
 INSERT INTO playbook_tests(agent_id,hypothesis_id,revision,test_index)
 VALUES(new.agent_id,new.id,new.revision,COALESCE((SELECT MAX(test_index) FROM playbook_tests WHERE agent_id=new.agent_id),0)+1);
END;
CREATE TABLE playbook_baselines (
 agent_id TEXT NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('post','reply')), threshold REAL NOT NULL CHECK(threshold>=0 AND threshold<=1),
 control_ids_json TEXT NOT NULL,
 n INTEGER NOT NULL CHECK(n=200), successes INTEGER NOT NULL CHECK(successes>=0 AND successes<=n), frozen_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,hypothesis_id,revision)
);
CREATE TABLE action_hypotheses (
 agent_id TEXT NOT NULL, action_id INTEGER NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 eligible INTEGER NOT NULL CHECK(eligible IN (0,1)),
 PRIMARY KEY(agent_id,action_id,hypothesis_id)
);
CREATE TABLE playbook_outcomes (
 agent_id TEXT NOT NULL, action_id INTEGER NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 measured_at TEXT NOT NULL, success INTEGER NOT NULL CHECK(success IN (0,1)),
 reason TEXT NOT NULL, counted INTEGER NOT NULL CHECK(counted IN (0,1)),
 PRIMARY KEY(agent_id,action_id,hypothesis_id)
);
CREATE TABLE playbook_decisions (
 agent_id TEXT NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('established','rejected')), n INTEGER NOT NULL, successes INTEGER NOT NULL,
 reason TEXT NOT NULL, decided_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,hypothesis_id,revision)
);

-- Each predeclared look is consumed once, including unavailable controls.
CREATE TABLE playbook_looks (
 agent_id TEXT NOT NULL, hypothesis_id INTEGER NOT NULL, revision INTEGER NOT NULL, n INTEGER NOT NULL,
 current_n INTEGER NOT NULL, current_successes INTEGER NOT NULL, control_ids_json TEXT NOT NULL,
 measured_at TEXT NOT NULL, status TEXT NOT NULL, reason TEXT NOT NULL,
 PRIMARY KEY(agent_id,hypothesis_id,revision,n)
);
