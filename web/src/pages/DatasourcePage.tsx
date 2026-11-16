import { useEffect, useState } from 'react'
import {
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
import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import {
  createDatasource,
  deleteDatasource,
  getCurrentUser,
  listDatasources,
  updateDatasource,
} from '@/api/client'
import type { ApplyScope, Datasource, DatasourceInput, DatasourceType } from '@/api/types'

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

export default function DatasourcePage() {
  const [items, setItems] = useState<Datasource[]>([])
  const [loading, setLoading] = useState(false)
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<Datasource | null>(null)
  const [form] = Form.useForm()
  // 实时监听类型，用于按类型调整账号字段的必填规则与文案
  const currentType: DatasourceType | undefined = Form.useWatch('type', form)
  const policy = currentType ? usernamePolicy[currentType] : usernamePolicy.postgresql

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
    form.resetFields()
    form.setFieldsValue({
      port: 5432,
      apply_enabled: false,
      max_apply_days: 7,
      apply_scopes: [],
    })
    setModalOpen(true)
  }

  const openEdit = (record: Datasource) => {
    setEditing(record)
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
          {r.description ? (
            <Text type="secondary" style={{ fontSize: 12 }}>
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
        // 没配账号说明这个数据源还是迁移前的旧数据，压根连不上，
        // 用醒目标签提示去补填，而不是显示一个无意义的 "-"
        if (!r.has_password || !v) {
          return (
            <Tag color="warning" style={{ marginInlineEnd: 0 }}>
              待配置账号密码
            </Tag>
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
          <Space direction="vertical" size={0}>
            <Tag color="success">已开放</Tag>
            <Text type="secondary" style={{ fontSize: 12 }}>
              最长 {r.max_apply_days} 天
            </Text>
          </Space>
        ) : (
          <Tag>未开放</Tag>
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
        onCancel={() => setModalOpen(false)}
        onOk={submit}
        width={760}
        okText="保存"
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
            <Form.Item name="database" label="默认库" style={{ flex: 1 }}>
              <Input placeholder="shop（可选）" />
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
              extra={policy.hint || undefined}
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
                  : '密码会加密后保存，保存后不可再查看'
              }
              style={{ flex: 1 }}
            >
              <Input.Password
                placeholder={editing && editing.has_password ? '不修改请留空' : '请输入密码'}
                autoComplete="new-password"
              />
            </Form.Item>
          </Space>

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
