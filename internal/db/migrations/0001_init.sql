-- DataWarden 阶段一建表脚本
-- 本脚本必须幂等：可重复执行而不报错（靠 IF NOT EXISTS）

CREATE TABLE IF NOT EXISTS app_user (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE,
    email         TEXT        NOT NULL DEFAULT '',
    password_hash TEXT        NOT NULL,
    role          TEXT        NOT NULL DEFAULT 'developer',
    status        TEXT        NOT NULL DEFAULT 'active',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS datasource (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    type       TEXT        NOT NULL,
    host       TEXT        NOT NULL,
    port       INT         NOT NULL,
    db_name    TEXT        NOT NULL DEFAULT '',
    secret_ref TEXT        NOT NULL,
    env        TEXT        NOT NULL DEFAULT 'prod',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS grant_record (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES app_user(id),
    datasource_id BIGINT      NOT NULL REFERENCES datasource(id),
    scope         TEXT        NOT NULL,
    action        TEXT        NOT NULL DEFAULT 'read',
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_grant_user ON grant_record (user_id) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGSERIAL PRIMARY KEY,
    actor_id   BIGINT      NOT NULL DEFAULT 0,
    actor_name TEXT        NOT NULL DEFAULT '',
    action     TEXT        NOT NULL,
    object     TEXT        NOT NULL DEFAULT '',
    detail     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    ip         TEXT        NOT NULL DEFAULT '',
    user_agent TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log (created_at DESC);

-- 审计日志防篡改：写入后禁止 UPDATE / DELETE
-- 这是审计系统的底线要求，不为了排错方便而移除
CREATE OR REPLACE FUNCTION audit_log_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
CREATE TRIGGER audit_log_no_update
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
