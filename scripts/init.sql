-- ============================================================
-- DataWarden 数据库初始化脚本
-- ============================================================
-- 用途：在一个全新的 PostgreSQL 实例上，一次性把 DataWarden 运行所需的
--       数据库、表结构、初始账号与示例数据准备好。
--
-- 使用方式（二选一）：
--   1) psql -h <host> -p 5432 -U <superuser> -f init.sql
--   2) 在任意 SQL 客户端里整段执行
--
-- 重要说明：
--   * 本脚本以**超级用户**身份执行。普通账号没有 CREATEDB 权限，会报错。
--   * 脚本整体幂等，可重复执行；已存在的库/表/账号不会被重复创建。
--     （唯一例外：示例数据用 ON CONFLICT 覆盖，重跑会重置示例账号密码）
--   * 应用启动时也会自动执行 internal/db/migrations 下的迁移，
--     所以如果你只想要表结构、不想要示例数据，可以跳过第 4 节。
-- ============================================================


-- ============================================================
-- 1. 创建数据库
-- ============================================================
-- 生产库与应用库分开：datawarden 存业务元数据，datawarden_test 供集成测试用。
-- 测试库会被反复建表删表，绝不能与业务库共用。

-- CREATE DATABASE 不能在事务里执行，也不能用 IF NOT EXISTS（PG 不支持），
-- 所以用 DO 块判断后再动态执行。
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'datawarden') THEN
        -- 请把下面的 OWNER 换成你实际使用的账号名
        EXECUTE 'CREATE DATABASE datawarden OWNER your_db_user ENCODING ''UTF8''';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'datawarden_test') THEN
        EXECUTE 'CREATE DATABASE datawarden_test OWNER your_db_user ENCODING ''UTF8''';
    END IF;
END
$$;

-- 切换到业务库。psql 用 \c，其他客户端请手动切库后继续执行第 2 节之后的语句。
-- \c datawarden


-- ============================================================
-- 2. 表结构
-- ============================================================
-- 以下内容与 internal/db/migrations/0001_init.sql、0002_apply_config.sql 保持一致。
-- 应用启动时会自动执行那两个文件，此处是为了让「不启动应用也能建好库」。
-- 两边的结构必须同步修改，改一处就要改另一处。

-- ------------------------------------------------------------
-- 2.1 用户表
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS app_user (
    id            BIGSERIAL   PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE,
    email         TEXT        NOT NULL DEFAULT '',
    password_hash TEXT        NOT NULL,
    -- 角色：developer / dba / superadmin
    role          TEXT        NOT NULL DEFAULT 'developer',
    -- 状态：只有 active 的账号能登录，其他值等于停用
    status        TEXT        NOT NULL DEFAULT 'active',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ------------------------------------------------------------
-- 2.2 数据源表
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS datasource (
    id           BIGSERIAL   PRIMARY KEY,
    name         TEXT        NOT NULL UNIQUE,
    -- 类型：mysql / postgresql / redis / mongodb
    type         TEXT        NOT NULL,
    host         TEXT        NOT NULL,
    port         INT         NOT NULL,
    -- SQL：库名；Redis：db 号（如 '37'）
    db_name      TEXT        NOT NULL DEFAULT '',
    -- 密钥引用，不存明文密码。格式 env:VAR_NAME 表示从环境变量读取
    secret_ref   TEXT        NOT NULL,
    env          TEXT        NOT NULL DEFAULT 'prod',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- 可申请配置（0002 迁移新增）
    apply_enabled  BOOLEAN NOT NULL DEFAULT false,
    max_apply_days INT     NOT NULL DEFAULT 7,
    -- 形如 [{"database":"public","tables":["orders"]}]
    -- tables 为空数组表示该库下全部表
    apply_scopes   JSONB   NOT NULL DEFAULT '[]'::jsonb,
    description    TEXT    NOT NULL DEFAULT ''
);

-- ------------------------------------------------------------
-- 2.3 生效授权表
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS grant_record (
    id            BIGSERIAL   PRIMARY KEY,
    user_id       BIGINT      NOT NULL REFERENCES app_user(id),
    datasource_id BIGINT      NOT NULL REFERENCES datasource(id),
    -- SQL：db:table；Redis：db号:key前缀
    scope         TEXT        NOT NULL,
    action        TEXT        NOT NULL DEFAULT 'read',
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_grant_user
    ON grant_record (user_id) WHERE revoked_at IS NULL;

-- ------------------------------------------------------------
-- 2.4 授权申请单
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS grant_request (
    id             BIGSERIAL   PRIMARY KEY,
    user_id        BIGINT      NOT NULL REFERENCES app_user(id),
    datasource_id  BIGINT      NOT NULL REFERENCES datasource(id),
    scope          TEXT        NOT NULL,
    days           INT         NOT NULL,
    reason         TEXT        NOT NULL DEFAULT '',
    -- 状态：pending / approved / rejected / canceled
    status         TEXT        NOT NULL DEFAULT 'pending',
    reviewer_id    BIGINT,
    review_comment TEXT        NOT NULL DEFAULT '',
    reviewed_at    TIMESTAMPTZ,
    -- 审批通过后指向生成的 grant_record
    grant_id       BIGINT      REFERENCES grant_record(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_grant_request_status
    ON grant_request (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_grant_request_user
    ON grant_request (user_id, created_at DESC);

-- ------------------------------------------------------------
-- 2.5 审计日志表
-- ------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGSERIAL   PRIMARY KEY,
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

-- ------------------------------------------------------------
-- 2.6 审计日志防篡改触发器
-- ------------------------------------------------------------
-- 审计系统的基本要求：写入后不可改、不可删。
-- 这条规则不要为了"排错方便"而移除，否则审计的法律效力就没了。
CREATE OR REPLACE FUNCTION audit_log_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_log_no_update ON audit_log;
CREATE TRIGGER audit_log_no_update
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();


-- ============================================================
-- 3. 权限授予
-- ============================================================
-- PostgreSQL 15 起，public schema 默认不再给普通用户 CREATE 权限。
-- 跳过这两句会直接导致迁移报 “permission denied for schema public”。
--
-- 请把 your_db_user 换成你的应用账号；如果你直接用超级用户运行，可以跳过。

GRANT ALL ON SCHEMA public TO your_db_user;
GRANT ALL ON ALL TABLES IN SCHEMA public TO your_db_user;
GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO your_db_user;
GRANT ALL ON ALL FUNCTIONS IN SCHEMA public TO your_db_user;

-- 保证以后新建的表也自动授权
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO your_db_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO your_db_user;


-- ============================================================
-- 4. 初始账号
-- ============================================================
-- 三个角色各一个账号，密码均为初始值，**部署到生产前必须修改**。
--
--   用户名   密码        角色
--   admin    admin123    superadmin（可管理一切，可审批）
--   dba      dba123      dba（可登记数据源、审批申请）
--   dev      dev123      developer（只能申请与查询）
--
-- 下面存的是 bcrypt 哈希，对应的就是上面这些明文密码。
-- 验证方式：登录接口会做 bcrypt.CompareHashAndPassword。

INSERT INTO app_user (username, email, password_hash, role, status)
VALUES
    ('admin', 'admin@datawarden.local',
     '$2a$10$Cyv4Hc8a3XybCB6hJbgHD.jtMIu5xBt1LCHnSpwR.FSqatpI2vGFS', 'superadmin', 'active'),
    ('dba',   'dba@datawarden.local',
     '$2a$10$KSz4x8SnCw//KL792ozZFerRQ.nDxbZGAcZJFnC3pvJn2BmkQWEIS', 'dba',        'active'),
    ('dev',   'dev@datawarden.local',
     '$2a$10$bGgo/vMVP4LUt3jaNPz/MOgDPhtaIv6cd0gNo2xyrO0ikqJPO2BL.', 'developer',  'active')
ON CONFLICT (username) DO UPDATE
    SET password_hash = EXCLUDED.password_hash,
        role          = EXCLUDED.role,
        status        = EXCLUDED.status;


-- ============================================================
-- 5. 示例数据源（可选，仅本地体验用）
-- ============================================================
-- 这些是示例配置，host 需要改成你自己的环境。
-- 如果不需要，整段删除即可，不影响功能。
--
-- secret_ref 写成 env:XXX 表示从环境变量 XXX 读取密码，
-- 这样密码不会落进数据库。对应在 .env.local 里配置，例如：
--   QUERY_DB_PASSWORD="xxx"
--   REDIS_PASSWORD="xxx"

INSERT INTO datasource
    (name, type, host, port, db_name, secret_ref, env,
     apply_enabled, max_apply_days, apply_scopes, description)
VALUES
    (
        '示例 PostgreSQL', 'postgresql', '127.0.0.1', 5432, 'datawarden',
        'env:QUERY_DB_PASSWORD', 'dev',
        true, 14,
        '[{"database":"public","tables":["dw_probe"]}]'::jsonb,
        '示例库，仅开放 dw_probe 表。请把 host 改成你自己的地址'
    ),
    (
        '示例 Redis', 'redis', '127.0.0.1', 6379, '37',
        'env:REDIS_PASSWORD', 'dev',
        true, 7,
        '[{"database":"37","tables":["user:1001"]}]'::jsonb,
        '示例 Redis，db37，仅开放 user:1001 前缀。请把 host 改成你自己的地址'
    )
ON CONFLICT (name) DO NOTHING;


-- ============================================================
-- 6. 验证
-- ============================================================
-- 执行以下查询确认结果，期望：
--   app_user        3 行
--   datasource      2 行
--   grant_request   0 行
--   audit_log       0 行

-- SELECT 'app_user' AS tbl, count(*) FROM app_user
-- UNION ALL SELECT 'datasource',    count(*) FROM datasource
-- UNION ALL SELECT 'grant_record',  count(*) FROM grant_record
-- UNION ALL SELECT 'grant_request', count(*) FROM grant_request
-- UNION ALL SELECT 'audit_log',     count(*) FROM audit_log;


-- ============================================================
-- 部署到生产前必做
-- ============================================================
-- 1. 改掉三个初始账号的密码（admin123 / dba123 / dev123 绝对不能留）
-- 2. 把第 1、3 节的 owner / 授权对象换成真实账号
-- 3. 第 5 节示例数据的 host 换成真实地址，或整段删除
-- 4. 连接串的 sslmode 从 disable 改为 require
-- 5. 确认 audit_log_no_update 触发器存在（这是审计不可篡改的唯一保障）
