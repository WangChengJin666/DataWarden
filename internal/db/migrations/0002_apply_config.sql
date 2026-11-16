-- 阶段一补充：可申请数据源的配置 + 申请单表
-- 幂等脚本，可重复执行

-- 数据源的「可申请配置」。
-- 管理员登记数据源时决定：是否开放申请、允许申请的库表范围、最长可申请天数。
ALTER TABLE datasource ADD COLUMN IF NOT EXISTS apply_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE datasource ADD COLUMN IF NOT EXISTS max_apply_days INT NOT NULL DEFAULT 7;
ALTER TABLE datasource ADD COLUMN IF NOT EXISTS apply_scopes JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE datasource ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

-- 申请单：开发者提交的授权申请，审批通过后落地为 grant_record。
CREATE TABLE IF NOT EXISTS grant_request (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES app_user(id),
    datasource_id BIGINT      NOT NULL REFERENCES datasource(id),
    scope         TEXT        NOT NULL,
    days          INT         NOT NULL,
    reason        TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL DEFAULT 'pending',
    reviewer_id   BIGINT,
    review_comment TEXT       NOT NULL DEFAULT '',
    reviewed_at   TIMESTAMPTZ,
    grant_id      BIGINT REFERENCES grant_record(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_grant_request_status
    ON grant_request (status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_grant_request_user
    ON grant_request (user_id, created_at DESC);
