import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Divider,
  Form,
  Input,
  InputNumber,
  message,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import {
  ApiOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import {
  createDatasource,
  deleteDatasource,
  getCurrentUser,
  listDatasources,
  testDatasourceConnection,
  updateDatasource,
} from '@/api/client'
import type {
  ApplyScope,
  Datasource,
  DatasourceInput,
  DatasourceType,
  TestConnResult,
} from '@/api/types'

const { Text } = Typography

const typeLabels: Record<DatasourceType, string> = {
  mysql: 'MySQL',
  postgresql: 'PostgreSQL',
  redis: 'Redis',
  mongodb: 'MongoDB',
}

const typeColors: Record<DatasourceType, string> = {
  mysql: 'blue',
  postgresql: 'geekblue',
  redis: 'red',
  mongodb: 'green',
}

// 各类型数据源对账号的要求。
// Redis 的默认认证方式只有密码（requirepass），没有账号概念；
// 平台内部的 Redis 客户端也不传用户名，所以这里不允许填，避免误导。
const usernamePolicy: Record<
  DatasourceType,
  { required: boolean; label: string; placeholder: string; hint: string }
> = {
  mysql: {
    required: true,
    label: '连接账号',
    placeholder: '例如 readonly',
    hint: '',
  },
  postgresql: {
    required: true,
    label: '连接账号',
    placeholder: '例如 readonly',
    hint: '',
  },
  mongodb: {
    required: true,
    label: '连接账号',
    placeholder: '例如 readonly',
    hint: '',
  },
  redis: {
    required: false,
    label: '连接账号（可选）',
    placeholder: 'Redis 通常不需要',
    hint: 'Redis 默认只用密码认证，账号留空即可。若你的实例启用了 ACL 且账号不是 default，才需要填写。',
  },
}

// 「库」字段在各类数据库里的语义不同，必填与否必须分开判断：
//
//   PostgreSQL  连接串为 postgres://user:pw@host:port/DBNAME，
//               库名是连接的一部分，留空会连到与用户同名的库并失败 → 必填
//   MySQL       DSN 为 user:pw@tcp(host:port)/DBNAME，库名可留空，
//               留空即「连接但不选库」，语句里写 db.table 完全可用 → 可选
//   Redis       字段含义是 db 号，留空等于 db 0 → 可选
//   MongoDB     可以连到实例而不指定库 → 可选
//
// 之前一律设成必填是错的：用户完全可以先登记 MySQL 数据源，
// 之后在查询里用 `库名.表名` 指定，不该被表单拦住。
const databasePolicy: Record<
  DatasourceType,
  { required: boolean; label: string; placeholder: string; hint: string }
> = {
  postgresql: {
    required: true,
    label: '默认库（必填）',
    placeholder: '例如 shop',
    hint: '',
  },
  mysql: {
    required: false,
    label: '默认库（可选）',
    placeholder: '可留空，查询时写库名.表名',
    hint: '',
  },
  redis: {
    required: false,
    label: 'db 号（可选）',
    placeholder: '不填为 0',
    hint: '',
  },
  mongodb: {
    required: true,
    label: '默认库（必填）',
    placeholder: '例如 test',
    hint: '',
  },
}

export default function DatasourcePage() {
  const [items, setItems] = useState<Datasource[]>([])
  const [loading, setLoading] = useState(false)
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<Datasource | null>(null)
  // 测试连接的状态：结果只用于展示，不写入任何持久化状态
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<TestConnResult | null>(null)
  const [form] = Form.useForm()
  // 实时监听类型，用于按类型调整账号字段的必填规则与文案
  const currentType: DatasourceType | undefined = Form.useWatch('type', form)
  const policy = currentType ? usernamePolicy[currentType] : usernamePolicy.postgresql
  const dbPolicy = currentType ? databasePolicy[currentType] : databasePolicy.postgresql

  const isDBA = ['dba', 'superadmin'].includes(getCurrentUser()?.role ?? '')

  const load = async () => {
    setLoading(true)
    try {
      setItems(await listDatasources())
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const openCreate = () => {
    setEditing(null)
    setTestResult(null)
    form.resetFields()
    form.setFieldsValue({
      // 给类型一个默认值：不预设的话「测试连接」会因类型必填而直接被拦，
      // 用户点了按钮却没有任何反馈，很难理解是哪里没填。
      type: 'postgresql',
      port: 5432,
      apply_enabled: false,
      max_apply_days: 7,
      apply_scopes: [],
    })
    setModalOpen(true)
  }

  const openEdit = (record: Datasource) => {
    setEditing(record)
    setTestResult(null)
    form.resetFields()
    form.setFieldsValue({
      name: record.name,
      type: record.type,
      host: record.host,
      port: record.port,
      database: record.database,
      username: record.username,
      // 密码不回填：后端不返回密码，留空即表示不修改
      password: undefined,
      apply_enabled: record.apply_enabled,
      max_apply_days: record.max_apply_days,
      apply_scopes: record.apply_scopes ?? [],
      description: record.description,
    })
    setModalOpen(true)
  }

  const submit = async () => {
    try {
      const values = await form.validateFields()
      const input: DatasourceInput = {
        ...values,
        apply_scopes: (values.apply_scopes ?? []).map((s: ApplyScope) => ({
          database: s?.database ?? '',
          tables: Array.isArray(s?.tables)
            ? s.tables
            : typeof s?.tables === 'string' && s.tables
              ? String(s.tables)
                  .split(',')
                  .map((x) => x.trim())
                  .filter(Boolean)
              : [],
        })),
      }
      if (editing) {
        // 编辑时密码留空表示不修改，交给后端保留原密文
        if (!input.password) delete input.password
        await updateDatasource(editing.id, input)
        message.success('已更新')
      } else {
        if (!input.password) {
          message.warning('请填写连接密码')
          return
        }
        await createDatasource(input)
        message.success('已创建')
      }
      setModalOpen(false)
      void load()
    } catch (e) {
      if ((e as { errorFields?: unknown }).errorFields) return
      message.error((e as Error).message)
    }
  }

  // testConnection 用当前表单值实连一次目标库。
  // 只看连接相关的字段，不要求「可申请配置」这类字段已填好——
  // 用户往往还没配完就想先确认网络和账号能不能通。
  const testConnection = async () => {
    setTestResult(null)
    let values
    try {
      // 只校验连接相关字段：用户往往还没配完「可申请范围」就想先确认能否连上。
      // 库名是否必填按类型决定（只有 PostgreSQL 必须填），见 databasePolicy。
      values = await form.validateFields(
        dbPolicy.required
          ? ['type', 'host', 'port', 'database']
          : ['type', 'host', 'port'],
      )
    } catch {
      return // 必填项没填，validateFields 已在表单上标红
    }

    if (!editing && !values.type) return

    setTesting(true)
    try {
      const res = await testDatasourceConnection({
        type: values.type,
        host: values.host,
        port: values.port,
        database: form.getFieldValue('database'),
        username: form.getFieldValue('username'),
        password: form.getFieldValue('password') || undefined,
        datasource_id: editing?.id,
      })
      setTestResult(res)
    } catch (e) {
      setTestResult({ ok: false, message: (e as Error).message })
    } finally {
      setTesting(false)
    }
  }

  const remove = async (id: number) => {
    try {
      await deleteDatasource(id)
      message.success('已删除')
      void load()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const columns: ColumnsType<Datasource> = [
    {
      title: '名称',
      dataIndex: 'name',
      width: 150,
      render: (v: string, r) => (
        <div>
          <div style={{ fontWeight: 500 }}>{v}</div>
          {/* 描述与名称相同时不重复显示，否则同一行出现两遍一样的字 */}
          {r.description && r.description !== v ? (
            <Text
              type="secondary"
              style={{ fontSize: 12 }}
              ellipsis={{ tooltip: r.description }}
            >
              {r.description}
            </Text>
          ) : null}
        </div>
      ),
    },
    {
      title: '类型',
      dataIndex: 'type',
      width: 110,
      render: (v: DatasourceType) => <Tag color={typeColors[v]}>{typeLabels[v] ?? v}</Tag>,
    },
    {
      title: '地址',
      width: 200,
      ellipsis: true,
      render: (_, r) => (
        <Text code style={{ fontSize: 12 }}>
          {r.host}:{r.port}
          {r.database ? `/${r.database}` : ''}
        </Text>
      ),
    },
    {
      title: '账号',
      dataIndex: 'username',
      width: 150,
      render: (v: string, r) => {
        // 是否「可用」要按类型判断：Redis 只用密码认证，没有账号是正常的；
        // 其他类型必须账号和密码都配齐。
        // 不配齐的数据源连不上，用醒目标签提示去补填，而不是显示无意义的 "-"
        const ready = usernamePolicy[r.type].required
          ? r.has_password && !!v
          : r.has_password

        if (!ready) {
          return (
            <Tag color="warning" style={{ marginInlineEnd: 0 }}>
              待配置账号密码
            </Tag>
          )
        }
        // Redis 且未填账号：说明用的是默认 default 用户，明确写出来避免误解为空
        if (!v) {
          return (
            <Text type="secondary" style={{ fontSize: 12 }}>
              default
            </Text>
          )
        }
        return (
          <Text code style={{ fontSize: 12 }}>
            {v}
          </Text>
        )
      },
    },
    {
      title: '可申请',
      dataIndex: 'apply_enabled',
      width: 140,
      render: (v: boolean, r) =>
        v ? (
          <Space direction="vertical" size={2}>
            {/* Tag 自带 margin-inline-end，竖向堆叠时会顶得文字偏左，这里清零 */}
            <Tag color="success" style={{ marginInlineEnd: 0 }}>
              已开放
            </Tag>
            <Text type="secondary" style={{ fontSize: 12 }}>
              最长 {r.max_apply_days} 天
            </Text>
          </Space>
        ) : (
          <Tag style={{ marginInlineEnd: 0 }}>未开放</Tag>
        ),
    },
    {
      title: '可申请范围',
      width: 210,
      render: (_, r) => {
        if (!r.apply_enabled) return <Text type="secondary">—</Text>
        if (!r.apply_scopes?.length) return <Text type="warning">未配置范围</Text>
        return (
          <Space direction="vertical" size={2}>
            {r.apply_scopes.slice(0, 2).map((s, i) => (
              <Text key={i} style={{ fontSize: 12 }}>
                {s.database}
                {s.tables?.length ? `: ${s.tables.join(', ')}` : ' (全部表)'}
              </Text>
            ))}
            {r.apply_scopes.length > 2 ? (
              <Text type="secondary" style={{ fontSize: 12 }}>
                等 {r.apply_scopes.length} 项
              </Text>
            ) : null}
          </Space>
        )
      },
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      width: 140,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '操作',
      width: 120,
      fixed: 'right',
      render: (_, r) =>
        isDBA ? (
          <Space>
            <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)}>
              编辑
            </Button>
            <Popconfirm
              title="确认删除该数据源？"
              description="若已被申请或授权引用，删除会失败。"
              onConfirm={() => remove(r.id)}
            >
              <Button size="small" danger icon={<DeleteOutlined />} />
            </Popconfirm>
          </Space>
        ) : (
          <Text type="secondary">—</Text>
        ),
    },
  ]

  return (
    <Card
      title="数据源管理"
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
          {isDBA ? (
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              新增数据源
            </Button>
          ) : null}
        </Space>
      }
    >
      {!isDBA ? (
        <Text type="secondary" style={{ display: 'block', marginBottom: 16 }}>
          当前角色为开发者，仅可查看数据源。管理操作需要 DBA 或超级管理员权限。
        </Text>
      ) : null}

      {isDBA ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="密码以明文保存"
          description={
            <span>
              各数据源的连接密码以明文存入平台元数据库，接口不会返回密码，但任何能读取
              <Text code>datasource</Text> 表的人（含 DBA、数据库备份文件、误发的 dump）
              都能直接看到。请为每个数据源使用<Text strong>只读账号</Text>，
              并对平台元数据库做好访问控制与备份加密。
            </span>
          }
        />
      ) : null}

      <Table
        rowKey="id"
        columns={columns}
        dataSource={items}
        loading={loading}
        scroll={{ x: 1220 }}
        pagination={{ pageSize: 20, showTotal: (t) => `共 ${t} 条` }}
      />

      <Modal
        title={editing ? '编辑数据源' : '新增数据源'}
        open={modalOpen}
        onCancel={() => {
          setModalOpen(false)
          setTestResult(null)
        }}
        onOk={submit}
        width={760}
        okText="保存"
        footer={(_, { OkBtn, CancelBtn }) => (
          <Space>
            <Button
              onClick={testConnection}
              loading={testing}
              icon={<ApiOutlined />}
            >
              测试连接
            </Button>
            <CancelBtn />
            <OkBtn />
          </Space>
        )}
        // forceRender 让表单在 Modal 首次打开前就挂载，
        // 这样 setFieldsValue 写入的值不会被丢弃。
        // 之前用 destroyOnClose，表单在关闭时被销毁，
        // 打开前 setFieldsValue 的值无处可依，导致编辑弹窗空白。
        forceRender
      >
        <Form form={form} layout="vertical">
          <Space size="middle" style={{ display: 'flex' }}>
            <Form.Item
              name="name"
              label="名称"
              rules={[{ required: true, message: '请输入名称' }]}
              style={{ flex: 1 }}
            >
              <Input placeholder="例如 shop-prod" />
            </Form.Item>
            <Form.Item
              name="type"
              label="类型"
              rules={[{ required: true, message: '请选择类型' }]}
              style={{ width: 160 }}
            >
              <Select
                options={Object.entries(typeLabels).map(([value, label]) => ({
                  value,
                  label,
                }))}
                onChange={(v: DatasourceType) => {
                  form.setFieldValue('port', v === 'mysql' ? 3306 : v === 'redis' ? 6379 : v === 'mongodb' ? 27017 : 5432)
                  // 切到不需要账号的类型时清掉残留值，避免把上一次填的账号带进 Redis
                  if (!usernamePolicy[v].required) {
                    form.setFieldValue('username', '')
                  }
                }}
              />
            </Form.Item>
          </Space>

          <Space size="middle" style={{ display: 'flex' }}>
            <Form.Item
              name="host"
              label="主机"
              rules={[{ required: true, message: '请输入主机' }]}
              style={{ flex: 1 }}
            >
              <Input placeholder="10.0.0.5" />
            </Form.Item>
            <Form.Item
              name="port"
              label="端口"
              rules={[{ required: true, message: '请输入端口' }]}
              style={{ width: 120 }}
            >
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item
              name="database"
              label={dbPolicy.label}
              rules={
                dbPolicy.required
                  ? [
                      {
                        required: true,
                        message:
                          currentType === 'mongodb'
                            ? 'MongoDB 必须填写默认库，否则无法确定要查哪个库'
                            : 'PostgreSQL 必须填写库名',
                      },
                    ]
                  : []
              }
              style={{ flex: 1 }}
            >
              <Input placeholder={dbPolicy.placeholder} />
            </Form.Item>
          </Space>

          <Space size="middle" style={{ display: 'flex', alignItems: 'flex-start' }}>
            <Form.Item
              name="username"
              label={policy.label}
              rules={
                policy.required
                  ? [{ required: true, message: '请输入连接账号' }]
                  : []
              }
              // 与密码字段的 extra 保持同高，否则同一行两个字段底部不齐
              extra={
                policy.required
                  ? '建议使用只读账号'
                  : 'Redis 通常不需要填账号'
              }
              style={{ flex: 1 }}
            >
              <Input placeholder={policy.placeholder} autoComplete="off" />
            </Form.Item>
            <Form.Item
              name="password"
              label="连接密码"
              rules={
                editing
                  ? []
                  : [{ required: true, message: '请输入连接密码' }]
              }
              extra={
                editing && editing.has_password
                  ? '留空表示不修改现有密码'
                  : '密码以明文存入平台库，请使用只读账号'
              }
              style={{ flex: 1 }}
            >
              <Input.Password
                placeholder={editing && editing.has_password ? '不修改请留空' : '请输入密码'}
                autoComplete="new-password"
              />
            </Form.Item>
          </Space>

          {testResult ? (
            <Alert
              style={{ marginBottom: 16 }}
              type={testResult.ok ? 'success' : 'error'}
              showIcon
              closable
              onClose={() => setTestResult(null)}
              message={testResult.ok ? '连接成功' : '连接失败'}
              description={
                <Space direction="vertical" size={0}>
                  {testResult.ok ? null : <Text>{testResult.message}</Text>}
                  {testResult.ok && testResult.elapsed_ms !== undefined ? (
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      耗时 {testResult.elapsed_ms} ms
                    </Text>
                  ) : null}
                </Space>
              }
            />
          ) : null}

          <Form.Item name="description" label="描述">
            <Input.TextArea rows={2} placeholder="给开发者看的说明，会显示在申请页" />
          </Form.Item>

          <Divider orientation="left" plain>
            可申请配置
          </Divider>

          <Space size="large" align="start">
            <Form.Item
              name="apply_enabled"
              label="开放申请"
              valuePropName="checked"
              extra="关闭后开发者无法在申请页看到该数据源"
            >
              <Switch />
            </Form.Item>
            <Form.Item
              name="max_apply_days"
              label="最长申请天数"
              rules={[{ required: true, message: '请设置天数' }]}
            >
              <InputNumber min={1} max={365} addonAfter="天" />
            </Form.Item>
          </Space>

          <Form.Item
            name="apply_scopes"
            label="允许申请的库表范围"
            extra="库名 + 表名列表；表名留空表示该库下全部表。未配置范围则任何人都无法申请。"
          >
            <ScopeList />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  )
}

/** 可增删的范围行编辑器，配合 Form.List 使用 */
function ScopeList({ value = [], onChange }: { value?: ApplyScope[]; onChange?: (v: ApplyScope[]) => void }) {
  const update = (idx: number, patch: Partial<ApplyScope>) => {
    const next = value.map((s, i) => (i === idx ? { ...s, ...patch } : s))
    onChange?.(next)
  }

  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      {value.map((scope, idx) => (
        <Space key={idx} style={{ width: '100%' }} align="start">
          <Input
            placeholder="库名，如 shop"
            value={scope.database}
            onChange={(e) => update(idx, { database: e.target.value })}
            style={{ width: 180 }}
          />
          <Input
            placeholder="表名，逗号分隔；留空表示全部表"
            value={Array.isArray(scope.tables) ? scope.tables.join(',') : ''}
            onChange={(e) =>
              update(idx, {
                tables: e.target.value
                  .split(',')
                  .map((x) => x.trim())
                  .filter(Boolean),
              })
            }
            style={{ width: 380 }}
          />
          <Button
            danger
            icon={<DeleteOutlined />}
            onClick={() => onChange?.(value.filter((_, i) => i !== idx))}
          />
        </Space>
      ))}
      <Button
        type="dashed"
        icon={<PlusOutlined />}
        onClick={() => onChange?.([...value, { database: '', tables: [] }])}
        block
      >
        添加范围
      </Button>
    </Space>
  )
}
