-- 数据源连接高级参数
--
-- 背景：真实环境的连接远不止 host/port/user/password。
--   * MongoDB 副本集只能填一个 host，必须带 replicaSet=
--   * 云数据库普遍要求 TLS（tls=true / tlsCAFile）
--   * 生产的只读查询通常挂从库（readPreference=secondaryPreferred）
--   * PostgreSQL 的 sslmode 之前硬编码成 disable，云实例连不上
--
-- 用 JSONB 存键值对而不是给每种参数加一列：
-- 不同库支持的参数差异很大，穷举列会让表结构随驱动版本无限膨胀，
-- 而且每加一个参数都要写一次迁移。键的白名单校验放在 adapter 层，
-- 数据库只负责存。
--
-- 默认 '{}' 而不是 NULL：读取端不必到处判空。
--
-- 幂等脚本，可重复执行。

ALTER TABLE datasource ADD COLUMN IF NOT EXISTS options JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN datasource.options IS
    '连接高级参数，键值透传给驱动（如 replicaSet/tls/sslmode），由 adapter 做白名单校验';
