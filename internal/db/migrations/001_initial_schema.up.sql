-- 001_initial_schema.up.sql
-- Orcastra v1 initial schema
-- PostgreSQL 16, UUIDv7 PKs, timestamptz everywhere, envelope encryption on secrets

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- =====================================================================
-- Identity & tenancy
-- =====================================================================
CREATE TABLE teams (
    id          uuid        PRIMARY KEY,
    name        text        NOT NULL,
    slug        citext      NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz
);

CREATE TABLE users (
    id              uuid        PRIMARY KEY,
    email           citext      NOT NULL UNIQUE,
    password_hash   text        NOT NULL,           -- argon2id
    display_name    text,
    is_root_admin   boolean     NOT NULL DEFAULT false,
    last_login_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    deleted_at      timestamptz
);

CREATE TYPE team_role AS ENUM ('owner', 'admin', 'developer', 'viewer');

CREATE TABLE team_members (
    team_id     uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role        team_role   NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_user_idx ON team_members(user_id);

CREATE TABLE api_tokens (
    id              uuid        PRIMARY KEY,
    user_id         uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id         uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name            text        NOT NULL,
    token_hash      bytea       NOT NULL UNIQUE,    -- SHA-256 of raw token
    token_prefix    text        NOT NULL,           -- first 8 chars for UX
    scopes          text[]      NOT NULL DEFAULT '{}',
    last_used_at    timestamptz,
    expires_at      timestamptz,
    revoked_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_tokens_user_idx ON api_tokens(user_id) WHERE revoked_at IS NULL;

-- =====================================================================
-- Servers
-- =====================================================================
CREATE TYPE server_status AS ENUM (
    'pending', 'validating', 'reachable', 'unreachable', 'disabled'
);

CREATE TABLE servers (
    id                   uuid          PRIMARY KEY,
    team_id              uuid          NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    name                 text          NOT NULL,
    host                 text          NOT NULL,
    port                 integer       NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
    ssh_user             text          NOT NULL DEFAULT 'root',
    ssh_key_ct           bytea         NOT NULL,    -- envelope-encrypted private key
    ssh_key_kid          text          NOT NULL,    -- key ID used to encrypt
    ssh_host_fingerprint text,                      -- pinned after first connect
    status               server_status NOT NULL DEFAULT 'pending',
    status_reason        text,
    last_seen_at         timestamptz,
    docker_version       text,
    os_info              jsonb,
    is_localhost         boolean       NOT NULL DEFAULT false,
    created_at           timestamptz   NOT NULL DEFAULT now(),
    updated_at           timestamptz   NOT NULL DEFAULT now(),
    deleted_at           timestamptz,
    UNIQUE (team_id, name)
);
CREATE INDEX servers_team_idx ON servers(team_id) WHERE deleted_at IS NULL;

-- =====================================================================
-- Projects & services
-- =====================================================================
CREATE TABLE projects (
    id          uuid        PRIMARY KEY,
    team_id     uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    server_id   uuid        NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
    name        text        NOT NULL,
    description text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,
    UNIQUE (team_id, name)
);
CREATE INDEX projects_team_idx ON projects(team_id) WHERE deleted_at IS NULL;

CREATE TYPE service_type AS ENUM ('app', 'database', 'worker', 'cron', 'static');

CREATE TABLE services (
    id              uuid            PRIMARY KEY,
    project_id      uuid            NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    team_id         uuid            NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name            text            NOT NULL,
    type            service_type    NOT NULL DEFAULT 'app',
    docker_image    text,
    git_repo_url    text,
    git_branch      text,
    dockerfile_path text            DEFAULT 'Dockerfile',
    domain          text,
    port            integer,
    created_at      timestamptz     NOT NULL DEFAULT now(),
    updated_at      timestamptz     NOT NULL DEFAULT now(),
    deleted_at      timestamptz,
    UNIQUE (project_id, name)
);
CREATE INDEX services_team_idx ON services(team_id) WHERE deleted_at IS NULL;

-- =====================================================================
-- Secrets (versioned-immutable, envelope-encrypted)
-- =====================================================================
CREATE TABLE secrets (
    id          uuid        PRIMARY KEY,
    service_id  uuid        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    team_id     uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    key         text        NOT NULL,
    value_ct    bytea       NOT NULL,   -- envelope-encrypted value
    value_kid   text        NOT NULL,   -- key ID used to encrypt
    version     integer     NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- immutable: never UPDATE, only INSERT new version
    UNIQUE (service_id, key, version)
);
CREATE INDEX secrets_service_idx ON secrets(service_id);

-- =====================================================================
-- Deployments (append-only, idempotency key)
-- =====================================================================
CREATE TYPE deployment_status AS ENUM (
    'queued', 'building', 'deploying', 'running', 'failed', 'cancelled', 'superseded'
);

CREATE TABLE deployments (
    id              uuid                PRIMARY KEY,
    service_id      uuid                NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    team_id         uuid                NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    idempotency_key text                NOT NULL,       -- (service_id, commit_sha)
    commit_sha      text,
    commit_message  text,
    triggered_by    uuid                REFERENCES users(id) ON DELETE SET NULL,
    trigger_type    text                NOT NULL,       -- 'webhook', 'manual', 'api'
    status          deployment_status   NOT NULL DEFAULT 'queued',
    started_at      timestamptz,
    finished_at     timestamptz,
    ai_diagnosis    text,                               -- LLM failure analysis
    ai_suggestion   text,                               -- LLM suggested fix
    created_at      timestamptz         NOT NULL DEFAULT now()
);
-- Enforce: only one active deploy per service at a time
CREATE UNIQUE INDEX deployments_idempotency_idx ON deployments(idempotency_key);
CREATE UNIQUE INDEX deployments_one_active_per_service
    ON deployments(service_id)
    WHERE status IN ('queued', 'building', 'deploying');
CREATE INDEX deployments_service_idx ON deployments(service_id, created_at DESC);

-- Secret versions used in a deployment (for rollback fidelity)
CREATE TABLE deployment_secrets (
    deployment_id   uuid NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    secret_id       uuid NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    PRIMARY KEY (deployment_id, secret_id)
);

-- =====================================================================
-- Deployment logs (append-only, separate table, partition-ready)
-- =====================================================================
CREATE TABLE deployment_logs (
    id              bigserial   PRIMARY KEY,
    deployment_id   uuid        NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    ts              timestamptz NOT NULL DEFAULT now(),
    stream          text        NOT NULL CHECK (stream IN ('stdout', 'stderr')),
    line            text        NOT NULL
);
CREATE INDEX deployment_logs_deployment_idx ON deployment_logs(deployment_id, ts);

-- =====================================================================
-- Domains
-- =====================================================================
CREATE TABLE domains (
    id              uuid        PRIMARY KEY,
    service_id      uuid        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    team_id         uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    fqdn            citext      NOT NULL UNIQUE,
    ssl_enabled     boolean     NOT NULL DEFAULT true,
    ssl_status      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- =====================================================================
-- Audit log (append-only, never UPDATE or DELETE)
-- =====================================================================
CREATE TABLE audit_events (
    id              bigserial   PRIMARY KEY,
    ts              timestamptz NOT NULL DEFAULT now(),
    actor_id        uuid        REFERENCES users(id) ON DELETE SET NULL,
    action          text        NOT NULL,   -- e.g. 'service.deploy', 'secret.create'
    resource_type   text        NOT NULL,
    resource_id     uuid,
    team_id         uuid        REFERENCES teams(id) ON DELETE SET NULL,
    ip              inet,
    request_id      text,
    meta            jsonb
);
CREATE INDEX audit_events_team_idx ON audit_events(team_id, ts DESC);
CREATE INDEX audit_events_actor_idx ON audit_events(actor_id, ts DESC);
