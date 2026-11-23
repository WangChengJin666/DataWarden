-- 数据源新增 MongoDB 认证库
--
-- 背景：MongoDB 的账号归属于「某个库」。账号建在 admin 是全局管理员，
-- 建在业务库则只对该库有效（库内专属用户）。
-- 认证时如果 authSource 填错，服务端一律回 AuthenticationFailed，
-- 前端只能看到「认证失败，请检查账号和密码」——而密码其实是对的。
--
-- 之前代码把 authSource 硬编码成 admin，导致「账号建在业务库」的实例
-- 永远连不上。这里把认证库变成数据源的一个字段，由使用者自己指定。
--
-- 默认值 ''：表示与 db_name 相同（mongosh 的默认行为）。
-- 已有数据不需要回填——空串就是「按默认库认证」，
-- 对原先能连通的 admin 账号，需要手动把 auth_source 改成 'admin'。
--
-- 幂等脚本，可重复执行。

ALTER TABLE datasource ADD COLUMN IF NOT EXISTS auth_source TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN datasource.auth_source IS
    '仅 MongoDB 使用：账号所在的认证库。留空表示与 db_name 相同；全局管理员账号应填 admin';
