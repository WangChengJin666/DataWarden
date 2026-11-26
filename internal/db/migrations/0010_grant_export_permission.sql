-- 导出权限：授权（申请审批通过时确定的）是否附带「导出查询结果」的权利
--
-- 产品口径：申请授权时可以勾选「可导出查询结果」，DBA 审批通过后，
-- 该数据源上的查询页才会出现「导出 Excel」入口；未勾选则该数据源
-- 只能在线查看。判定粒度就是**单个数据源的一条授权**——
-- A 数据源批了导出、B 数据源没批，是同一个人身上完全正常的组合，
-- 所以这个标记必须跟着授权记录走，不能放在用户或组上。
--
-- 1) grant_record.can_export：这条授权是否允许导出。
--    默认 false：审批人没勾就是不批，fail-closed。
--    存量授权一律为 false——它们当初的审批里根本没有导出这一项，
--    事后默认放开等于把没人审查过的能力直接授予全体持权人。
--    需要的人重新申请一次（申请页届时可以勾上导出）。
--
--    注意：它**不进 Casbin**。导出不是"能不能查"的权限，而是查询之后
--    对结果的动作，鉴权位置在 httpapi 的导出接口（querysvc.Enforce 不参与）。
--    若把 act 扩成 "export"，SyncGrants 要为每条授权多写一条策略，
--    而导出判定本身只需要一个布尔值，两套事实源（策略表 + 列）迟早对不上。
--
-- 2) grant_request.can_export：申请人当时的诉求，留痕用。
--    审批通过时**复制**到 grant_record，而不是审批时让 DBA 重新勾选：
--    审批人看到的是"申请人申请了导出"，据此批或不批，语义才和界面一致。
--    DBA 若不想批导出，驳回后让申请人去掉勾选重提，或直接在"授权管理"
--    页把该授权的导出关掉（见下）。
--
--    为什么不复用现有的 action 列：action 已经是 'read'，且被
--    Casbin 策略的 act 段直接使用，改成 'read,export' 这类组合值会
--    让 Enforce 的 act 匹配出现歧义（同一个字段两种语义）。
--
-- 关于 COMMENT ON COLUMN 会被每次启动重跑：PG 里对相同文本的 COMMENT
-- 是空更新（不重写、不留死元组），拿到的是瞬时锁，可以接受。
-- 但新增列时**必须**保留 IF NOT EXISTS，否则第二次启动就直接报错。
--
-- 幂等脚本，可重复执行。

ALTER TABLE grant_record  ADD COLUMN IF NOT EXISTS can_export BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE grant_request ADD COLUMN IF NOT EXISTS can_export BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN grant_record.can_export IS
    '这条授权是否允许导出查询结果（Excel）。来源是申请单的勾选，审批通过时复制。
     false = 只能在线查看。存量授权为 false：审批时没有审查过导出，不追认。
     该标记不生成 Casbin 策略，导出判定在 httpapi 导出接口里做。';
COMMENT ON COLUMN grant_request.can_export IS
    '申请人是否要求导出查询结果的权限。留痕用，审批通过时复制到 grant_record。';

-- 授权管理页要按"能不能导出"筛选/展示，且导出判定要按 (user, datasource)
-- 查一次布尔值；现有索引是 (user_id) WHERE revoked_at IS NULL 的部分索引，
-- 已覆盖该查询的过滤条件，数据源维度再补一个联合索引避免全表扫。
CREATE INDEX IF NOT EXISTS idx_grant_user_ds ON grant_record (user_id, datasource_id);
