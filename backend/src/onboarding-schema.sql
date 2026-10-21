CREATE TABLE IF NOT EXISTS enrollments(
 id TEXT PRIMARY KEY, agent_id TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
 proof_hash TEXT NOT NULL, code_hash TEXT NOT NULL, agent_token_hash TEXT NOT NULL,
 expires_at INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
 session_id TEXT REFERENCES mobile_sessions(id) ON DELETE CASCADE,
 mobile_token_hash TEXT, claimed_at INTEGER
);
CREATE INDEX IF NOT EXISTS enrollments_expiry ON enrollments(expires_at);

CREATE TABLE IF NOT EXISTS workspace_managers(
 session_id TEXT PRIMARY KEY REFERENCES mobile_sessions(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE
);
