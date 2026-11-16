-- 数据源改为直接保存连接账号与加密后的密码
--
-- 变更内容：
--   * 新增 username      连接目标库使用的账号
--   * 新增 password_enc  密码的 AES-GCM 密文（base64），绝不存明文
--   * 移除 secret_ref    不再引用外部密钥管理
--   * 移除 env           本平台只面向生产环境，不需要区分环境
--
-- 幂等脚本，可重复执行。
-- 注意：DROP COLUMN IF EXISTS 会丢失旧列数据。stage 一阶段的老数据（secret_ref
-- 指向环境变量）无法自动迁移成密文，因为历史明文密码平台从未保存过。
-- 升级后需要管理员在界面上重新填写各数据源的账号密码。

ALTER TABLE datasource ADD COLUMN IF NOT EXISTS username     TEXT NOT NULL DEFAULT '';
ALTER TABLE datasource ADD COLUMN IF NOT EXISTS password_enc TEXT NOT NULL DEFAULT '';

ALTER TABLE datasource DROP COLUMN IF EXISTS secret_ref;
ALTER TABLE datasource DROP COLUMN IF EXISTS env;
