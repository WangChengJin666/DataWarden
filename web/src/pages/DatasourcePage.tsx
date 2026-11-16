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

export default function DatasourcePage() {
  const [items, setItems] = useState<Datasource[]>([])
  const [loading, setLoading] = useState(false)
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<Datasource | null>(null)
  const [form] = Form.useForm()

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
      env: 'prod',
      apply_enabled: false,
      max_apply_days: 7,
      apply_scopes: [],
    })
    setModalOpen(true)
  }

  const openEdit = (record: Datasource) => {
    setEditing(record)
    form.setFieldsValue({
      name: record.name,
      type: record.type,
      host: record.host,
      port: record.port,
      database: record.database,
      secret_ref: record.secret_ref,
      env: record.env,
      apply_enabled: record.apply_enabled,
      max_apply_days: record.max_apply_days,
      apply_scopes: record.apply_scopes,
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
        await updateDatasource(editing.id, input)
        message.success('已更新')
      } else {
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
      width: 160,
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
      width: 240,
      ellipsis: true,
      render: (_, r) => (
        <Text code style={{ fontSize: 12 }}>
          {r.host}:{r.port}
          {r.database ? `/${r.database}` : ''}
        </Text>
      ),
    },
    { title: '环境', dataIndex: 'env', width: 80 },
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
      width: 240,
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
      width: 150,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '操作',
      width: 130,
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
        scroll={{ x: 1200 }}
        pagination={{ pageSize: 20, showTotal: (t) => `共 ${t} 条` }}
      />

      <Modal
        title={editing ? '编辑数据源' : '新增数据源'}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={submit}
        width={760}
        okText="保存"
        destroyOnClose
      >
        <Form form={form} layout="vertical" preserve={false}>
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
                }}
              />
            </Form.Item>
            <Form.Item name="env" label="环境" style={{ width: 120 }}>
              <Select
                options={[
                  { value: 'prod', label: '生产' },
                  { value: 'staging', label: '预发' },
                  { value: 'test', label: '测试' },
                  { value: 'dev', label: '开发' },
                ]}
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

          <Form.Item
            name="secret_ref"
            label="凭据引用"
            rules={[{ required: true, message: '请输入凭据引用' }]}
            extra="指向密钥管理服务的标识，平台不保存明文密码"
          >
            <Input placeholder="secret/shop-prod 或 env" />
          </Form.Item>

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
