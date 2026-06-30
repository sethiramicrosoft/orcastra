-- 002_ai_provider_config.up.sql
-- AI provider config per team, envelope-encrypted API key

CREATE TABLE ai_provider_configs (
    id              uuid        PRIMARY KEY,
    team_id         uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    provider_type   text        NOT NULL,   -- 'openai_compat', 'anthropic', 'gemini', 'ollama'
    display_name    text        NOT NULL,   -- 'OpenRouter', 'Groq', 'Ollama', 'Custom', etc.
    base_url        text,                   -- null = use provider default
    model           text        NOT NULL,
    api_key_ct      bytea,                  -- envelope-encrypted, null for Ollama
    api_key_kid     text,                   -- key ID, null for Ollama
    is_enabled      boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (team_id)  -- one active provider per team in v1
);
