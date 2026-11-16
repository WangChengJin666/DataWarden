-- 数据源密码改为明文保存
--
-- 变更内容：
--   * password_enc 重命名为 password
--   * 语义从「AES-GCM 密文」变成「明文」
--
-- 为什么改名而不只是改注释：
--   列名带 _enc 会让人以为存的是密文，后来的人照着这个名字写代码、
--   做审计、写运维脚本，都会得到错误结论。名字必须如实反映内容。
--
-- 为什么老数据直接清空：
--   0003 写入的是 AES-GCM 密文，主密钥来自环境变量 DATAWARDEN_MASTER_KEY。
--   迁移执行时主密钥未必还在（换机器、换环境、从未配置都可能），
--   无法可靠解密。与其留下一堆解不开的数据让查询时报「密码错误」，
--   不如明确清空——界面上会显示「未配置密码」，管理员一看就知道要重填。
--
-- 幂等脚本，可重复执行。

DO $$
BEGIN
    -- 已有密文列、还没有明文列：改名
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'datasource' AND column_name = 'password_enc'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'datasource' AND column_name = 'password'
    ) THEN
        ALTER TABLE datasource RENAME COLUMN password_enc TO password;
        -- 改名后列里还是密文，无法解密，清空
        UPDATE datasource SET password = '';
    END IF;

    -- 兼容全新安装：init.sql 直接建 password 列，这里保证列一定存在
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'datasource' AND column_name = 'password'
    ) THEN
        ALTER TABLE datasource ADD COLUMN password TEXT NOT NULL DEFAULT '';
    END IF;
END $$;

COMMENT ON COLUMN datasource.password IS '连接目标库的密码，明文保存。平台不做加密，请通过数据库访问控制与备份加密来保护';
