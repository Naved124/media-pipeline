CREATE TABLE jobs (job_id TEXT PRIMARY KEY, 
status TEXT NOT NULL CHECK(status IN ('pending', 'processing', 'completed', 'failed', 'dead_lettered')),
input_key TEXT NOT NULL,
output_keys JSONB,
created_at TIMESTAMPTZ DEFAULT now(),
started_at TIMESTAMPTZ,
completed_at TIMESTAMPTZ,
error_message TEXT );
