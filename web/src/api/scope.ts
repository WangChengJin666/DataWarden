/**
 * 授权范围（scope）的展示辅助。
 *
 * 后端把 scope 存在 `grant_record` / `grant_request` 里，可能出现的形态：
 *   - `"*"`        整个数据源（申请审批通过后落这种，见 authz.ScopeAllDatabases）
 *   - `"*:*"`      同上。后端 NormalizeScope 会**拒绝**这种写法（返回空串），
 *                  但手工改库、旧版本或外部脚本可能写进去。鉴权侧
 *                  SyncGrants 会跳过它，属于「显示得对、实际不生效」的脏数据——
 *                  这里仍按整源展示，是为了不让 DBA 把它误读成一条普通库级授权，
 *                  同时在页面上有机会被发现（它和正常整源记录长得一样）。
 *   - `"<库>:*"`   改口径前的库级授权，鉴权仍然兼容
 *
 * 为什么要统一在这里判断而不是各页各写 `v === '*'`：
 * 同一种权限在两个页面显示成不同样式，DBA 据此判断范围就会出错；
 * 集中一处判断才不会出现「改了一个页面忘了另一个」。
 */

/** 整数据源授权可识别的写法。 */
const ALL_DATABASES_SCOPES = new Set(['*', '*:*'])

/** scope 是否表示「整个数据源」（所有库与表）。 */
export function isAllDatabasesScope(scope: string | null | undefined): boolean {
  return typeof scope === 'string' && ALL_DATABASES_SCOPES.has(scope.trim())
}
