-- 授权范围归一：数据源不再配置「允许申请的库表范围」
--
-- 产品决策：数据源登记时不填范围 = 该数据源下的库/表都可以申请，
-- 有该数据源的授权即可访问。逐库逐表维护白名单既没人维护，
-- 也给不出比库级鉴权更强的保证（权限判定只看库，见 querysvc）。
--
-- 1) 申请单的 scope 收敛成「整库」形态 <库>:*，与 Casbin object
--    ds:<id>:<库>:* 同构。存量表级申请（"shop:orders"）一并归一：
--    表级 scope 生成的策略是 ds:<id>:<库>:<表>，与整库策略是两套语义，
--    授权粒度已经改成库，留着只会让「授权管理」页显示得比实际更严。
-- 2) datasource.apply_scopes 列作废但保留：不填即全部，读取侧不再使用。
--    这里不清空存量值，只加注释说明，出问题时还能对照历史配置。
-- 幂等脚本，可重复执行。

UPDATE grant_request
   SET scope = split_part(scope, ':', 1) || ':*'
 WHERE position(':' IN scope) > 0
   AND scope NOT LIKE '%:*';

COMMENT ON COLUMN datasource.apply_scopes IS
    '已废弃：授权范围由数据源整体决定，不再配置库表白名单。仅保留历史值，读取侧忽略。';
COMMENT ON COLUMN grant_request.scope IS
    '申请的库，形态 <库>:*。表级 scope 已废弃（授权粒度是库）。';
