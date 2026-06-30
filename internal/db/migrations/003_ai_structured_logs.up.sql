-- 003_ai_structured_logs.up.sql
-- Extend deployment_logs for AI consumption.
-- Raw lines are kept. Structured fields added for pattern detection.

ALTER TABLE deployment_logs
    ADD COLUMN level       text,       -- 'error', 'warn', 'info', null if unknown
    ADD COLUMN source      text,       -- 'docker_build', 'docker_run', 'caddy', 'orcastra'
    ADD COLUMN parsed_meta jsonb;      -- extracted fields (exit_code, image, layer, etc.)

-- AI analysis results are stored per deployment (already in deployments table).
-- Add deploy_context so LLM has richer input without re-fetching across tables.
ALTER TABLE deployments
    ADD COLUMN deploy_context jsonb;   -- snapshot: service config, recent failures, git diff summary

-- Pattern index: which deployments had AI analysis run
CREATE INDEX deployments_ai_idx ON deployments(service_id, created_at DESC)
    WHERE ai_diagnosis IS NOT NULL;
