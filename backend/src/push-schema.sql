CREATE TABLE push_subscriptions(
 id TEXT PRIMARY KEY,
 session_id TEXT NOT NULL REFERENCES mobile_sessions(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 device_token TEXT NOT NULL,
 environment TEXT NOT NULL CHECK(environment IN ('sandbox','production')),
 kinds TEXT NOT NULL,
 enabled_since INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 revision TEXT NOT NULL,
 UNIQUE(session_id,agent_id), UNIQUE(environment,device_token,agent_id)
);
CREATE TABLE push_jobs(
 id TEXT PRIMARY KEY,
 subscription_id TEXT NOT NULL REFERENCES push_subscriptions(id) ON DELETE CASCADE,
 event_id TEXT NOT NULL,
 payload TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt INTEGER NOT NULL,
 lease TEXT,
 lease_until INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL DEFAULT 'pending',
 last_reason TEXT,
 UNIQUE(subscription_id,event_id)
);
CREATE INDEX push_due ON push_jobs(state,next_attempt,lease_until);
