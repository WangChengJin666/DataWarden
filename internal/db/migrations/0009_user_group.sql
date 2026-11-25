-- 组管理：给用户加归属组
--
-- 语义边界（重要）：组只是归类，不承载任何数据权限。
-- 授权仍然是「用户 + 数据源 + 库」，与用户属于哪个组无关。
-- 若将来有人在组上挂权限，必须同时改 querysvc 的鉴权路径与
-- authz.SyncGrants，否则会得到「组里加人却不生效」这种没有报错的错误。
--
-- ON DELETE SET NULL：删组不删人，组员自动变成「未分组」。
-- 这条约束放在数据库上而不是只靠应用层写 UPDATE，
-- 是因为「删组顺手把人删了」是这类功能最常见的事故，
-- 让它由外键行为兜底，比靠调用方记得写更好。
--
-- 幂等脚本，可重复执行。

CREATE TABLE IF NOT EXISTS user_group (
    id          BIGSERIAL   PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE app_user ADD COLUMN IF NOT EXISTS group_id BIGINT
    REFERENCES user_group(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_app_user_group ON app_user (group_id);

COMMENT ON TABLE user_group IS
    '用户组（部门）。仅用于归类，不承载数据权限';
COMMENT ON COLUMN app_user.group_id IS
    '所属组。NULL 表示未分组；组被删除时置 NULL，用户本身不删';
COMMENT ON COLUMN app_user.status IS
    'active=可登录 / disabled=停用可恢复 / deleted=逻辑删除不可恢复。
     注意：deleted 在库里有对应数据行（历史授权与审批记录靠 user_id 关联），
     不要用 DELETE FROM app_user 清理账号。';
COMMENT ON COLUMN app_user.username IS
    '登录名，全局唯一。账号被逻辑删除时改写为 <原名>#deleted#<id>，
     从而释放原名给新账号；# 不在允许的用户名字符集内，不会与正常用户冲突';

-- 已删除账号的清点视图：运维接手时第一件事就是确认「谁被删了、原名是什么」。
-- 用户名已被改写，靠肉眼认不出来，用视图把原名还原出来。
-- CREATE OR REPLACE 保证幂等。
CREATE OR REPLACE VIEW v_deleted_user AS
SELECT id,
       split_part(username, '#deleted#', 1) AS original_username,
       username                             AS stored_username,
       role,
       created_at
  FROM app_user
 WHERE status = 'deleted';

COMMENT ON VIEW v_deleted_user IS
    '已逻辑删除的账号清单。stored_username 带 #deleted#<id> 后缀，original_username 是重命名前的登录名';
