import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Input,
  InputNumber,
  message,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
} from 'antd'
import { PlayCircleOutlined } from '@ant-design/icons'
import { executeRedis, executeSQL, listDatasources } from '@/api/client'
import type { Datasource, QueryResult, RedisQueryResult } from '@/api/types'

const { Text, Paragraph } = Typography

export default function QueryPage() {
  const [datasources, setDatasources] = useState<Datasource[]>([])
  const [datasourceId, setDatasourceId] = useState<number>()
  const [database, setDatabase] = useState('')
  const [tables, setTables] = useState('')
  const [sql, setSql] = useState('')
  const [rowLimit, setRowLimit] = useState(100)
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<QueryResult | null>(null)
  const [redisMode, setRedisMode] = useState<'exact' | 'scan'>('exact')
  const [redisKey, setRedisKey] = useState('')
  const [redisResult, setRedisResult] = useState<RedisQueryResult | null>(null)

  useEffect(() => {
    void (async () => {
      try {
        const list = await listDatasources()
        setDatasources(list)
        const firstSql = list.find((d) => d.type === 'postgresql' || d.type === 'mysql')
        if (firstSql) setDatasourceId(firstSql.id)
      } catch (e) {
        message.error((e as Error).message)
      }
    })()
  }, [])

  const current = datasources.find((d) => d.id === datasourceId)
  const isRedis = current?.type === 'redis'

  const run = async () => {
    if (!datasourceId) {
      message.warning('请先选择数据源')
      return
    }
    if (!isRedis) {
      if (!database.trim()) {
        message.warning('请填写库名')
        return
      }
      if (!tables.trim()) {
        message.warning('请填写涉及的表名，平台需要逐表鉴权')
        return
      }
      if (!sql.trim()) {
        message.warning('请输入 SQL')
        return
      }
    } else if (!redisKey.trim()) {
      message.warning('请输入 key 或 key 模式')
      return
    } else if (redisMode === 'exact' && /[*?[]/.test(redisKey)) {
      // 用户在「精确 Key」里填了通配符，多半是想模糊查询，明确引导而不是静默返回空
      message.warning('精确查询不支持通配符，请切换到「模糊 Key」标签页')
      return
    } else if (redisMode === 'scan' && !/[*?[]/.test(redisKey)) {
      message.warning('模糊查询需要带通配符（如 user:1001:*），精确 key 请用「精确 Key」')
      return
    } else if (redisMode === 'scan') {
      // 与服务端规则保持一致：取第一个通配符之前的部分，去掉分隔符后至少 2 个字符
      const prefix = redisKey.split(/[*?[]/)[0].replace(/[:.\-_/]/g, '')
      if (prefix.length < 2) {
        message.warning('模糊查询的前缀至少需要 2 个有效字符，否则扫描范围过宽')
        return
      }
    }

    setLoading(true)
    setResult(null)
    setRedisResult(null)
    try {
      if (isRedis) {
        const payload =
          redisMode === 'exact'
            ? { datasource_id: datasourceId, key: redisKey.trim() }
            : { datasource_id: datasourceId, pattern: redisKey.trim() }
        const res = await executeRedis(payload)
        setRedisResult(res)
        return
      }
      const res = await executeSQL({
        datasource_id: datasourceId,
        database: database.trim(),
        tables: tables.split(',').map((t) => t.trim()).filter(Boolean),
        sql,
        row_limit: rowLimit,
      })
      setResult(res)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  // Redis 结果：key / 类型 / 值 / TTL。hash 类型把 fields 展开成一行。
  const redisTable = redisResult ? (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Space wrap>
        <Text type="secondary">
          返回 {redisResult.items.length} 个 key，耗时 {redisResult.elapsed_ms} ms
        </Text>
        <Tag color={redisResult.exact ? 'blue' : 'default'}>
          {redisResult.exact ? '精确查询' : 'SCAN 模糊查询'}
        </Tag>
        {redisResult.truncated ? <Tag color="warning">结果已截断</Tag> : null}
      </Space>
      <Table
        size="small"
        rowKey={(r) => r.key}
        dataSource={redisResult.items}
        locale={{ emptyText: '没有匹配的 key' }}
        columns={[
          {
            title: 'Key',
            dataIndex: 'key',
            width: 260,
            ellipsis: true,
            render: (v: string) => <Text code>{v}</Text>,
          },
          {
            title: '类型',
            dataIndex: 'type',
            width: 100,
            render: (v: string) => <Tag>{v}</Tag>,
          },
          {
            title: '值',
            ellipsis: true,
            render: (_, r) =>
              r.fields ? (
                <Text style={{ fontSize: 13 }}>
                  {Object.entries(r.fields)
                    .map(([k, val]) => `${k}=${val}`)
                    .join(', ')}
                </Text>
              ) : (
                <Text style={{ fontSize: 13 }}>{r.value ?? ''}</Text>
              ),
          },
          {
            title: 'TTL',
            dataIndex: 'ttl',
            width: 100,
            render: (v: number) =>
              v > 0 ? <Text type="secondary">{v}s</Text> : <Text type="secondary">永久</Text>,
          },
        ]}
        scroll={{ x: 'max-content', y: 420 }}
        pagination={{ pageSize: 50, showSizeChanger: false }}
      />
      <Text type="secondary" style={{ fontSize: 12 }}>
        提示：Redis 结果不做脱敏（值是二进制安全的原始内容），授权范围本身已限制可见的 key 前缀。
      </Text>
    </Space>
  ) : null

  const resultTable = result ? (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Space>
        <Text type="secondary">
          返回 {result.row_count} 行，耗时 {result.elapsed_ms} ms
        </Text>
        {result.truncated ? <Tag color="warning">结果已截断</Tag> : null}
      </Space>
      <Table
        size="small"
        rowKey={(_, i) => String(i)}
        dataSource={result.rows.map((row, i) => ({ __idx: i, row }))}
        columns={result.columns.map((col, ci) => ({
          title: col,
          dataIndex: ['row', ci],
          ellipsis: true,
          render: (v: unknown) =>
            v === null || v === undefined ? (
              <Text type="secondary">NULL</Text>
            ) : (
              String(v)
            ),
        }))}
        scroll={{ x: 'max-content', y: 420 }}
        pagination={{ pageSize: 50, showSizeChanger: false }}
      />
    </Space>
  ) : null

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Card title="数据查询">
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="查询规则"
          description={
            <span>
              只能执行只读语句（SELECT / SHOW / DESC / EXPLAIN）。必须填写涉及的表名，
              平台会逐表校验权限，任一表无权限则整条查询被拒绝。结果中的手机号、身份证、邮箱等字段会自动脱敏。
            </span>
          }
        />

        <Space wrap style={{ marginBottom: 12 }}>
          <Select
            style={{ width: 240 }}
            placeholder="选择数据源"
            value={datasourceId}
            onChange={setDatasourceId}
            options={datasources.map((d) => ({
              value: d.id,
              label: `${d.name} (${d.type})`,
            }))}
          />
          {!isRedis ? (
            <>
              <Input
                placeholder="库名，如 public"
                value={database}
                onChange={(e) => setDatabase(e.target.value)}
                style={{ width: 160 }}
              />
              <Input
                placeholder="涉及的表，逗号分隔"
                value={tables}
                onChange={(e) => setTables(e.target.value)}
                style={{ width: 240 }}
              />
              <InputNumber
                min={1}
                max={1000}
                value={rowLimit}
                onChange={(v) => setRowLimit(v ?? 100)}
                addonBefore="限"
                addonAfter="行"
                style={{ width: 170 }}
              />
            </>
          ) : null}
        </Space>

        {isRedis ? (
          <Card size="small" type="inner" title="Redis 查询">
            <Tabs
              activeKey={redisMode}
              onChange={(k) => setRedisMode(k as 'exact' | 'scan')}
              items={[
                {
                  key: 'exact',
                  label: '精确 Key',
                  children: (
                    <Input
                      placeholder="完整 key，如 user:1001:profile"
                      value={redisKey}
                      onChange={(e) => setRedisKey(e.target.value)}
                    />
                  ),
                },
                {
                  key: 'scan',
                  label: '模糊 Key',
                  children: (
                    <Space direction="vertical" style={{ width: '100%' }}>
                      <Input
                        placeholder="带前缀的模式，如 user:1001:*"
                        value={redisKey}
                        onChange={(e) => setRedisKey(e.target.value)}
                      />
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        必须带至少 2 个字符的前缀。平台使用 SCAN 游标遍历，绝不使用 KEYS 命令。
                      </Text>
                    </Space>
                  ),
                },
              ]}
            />
          </Card>
        ) : (
          <Input.TextArea
            rows={8}
            placeholder="SELECT id, phone FROM orders WHERE created_at > now() - interval '1 day' LIMIT 100"
            value={sql}
            onChange={(e) => setSql(e.target.value)}
            style={{ fontFamily: 'Menlo, Consolas, monospace', fontSize: 13 }}
          />
        )}

        <div style={{ marginTop: 16 }}>
          <Button type="primary" icon={<PlayCircleOutlined />} onClick={run} loading={loading}>
            执行
          </Button>
        </div>
      </Card>

      {result ? <Card title="查询结果">{resultTable}</Card> : null}
      {redisResult ? <Card title="Redis 查询结果">{redisTable}</Card> : null}

      <Paragraph type="secondary" style={{ fontSize: 12 }}>
        提示：所有查询都会记录审计日志，包含 SQL、涉及表、返回行数与耗时。
      </Paragraph>
    </Space>
  )
}
