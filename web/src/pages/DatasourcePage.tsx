import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Collapse,
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
  getDatasource,
  listDatasources,
  testDatasourceConnection,
  updateDatasource,
} from '@/api/client'
import type {
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

/**
 * 去掉空值的高级参数项。
 *
 * Form.Item 用 Select allowClear 清空后留下的是 undefined，
 * 直接提交会变成 {"replicaSet": undefined}。后端遍历时 `v == ""` 拦不住
 * undefined 的 JSON 序列化差异，所以统一在这里清一遍。
 */
function cleanOptions(raw: unknown): Record<string, string> | undefined {
  if (!raw || typeof raw !== 'object') return undefined
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
    if (v !== undefined && v !== null && String(v).trim() !== '') {
      out[k] = String(v).trim()
    }
  }
  return Object.keys(out).length ? out : undefined
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
    // 这个字段是**连接用的 database 名**（DSN 的 dbname），
    // 不是查询页里选的那个「模式（schema）」。两者在 PG 下是不同概念，
    // 但共用了一个输入框——label 必须说清是哪一种，
    // 否则用户会照查询页看到的下拉（public 等 schema）来填，
    // 存进库后 DSN 变成 dbname=public，直接连不上。
    label: '数据库名（必填，连接用）',
    placeholder: '例如 postgres 或业务库名',
    hint: '这是连接时用的 database 名；查询页里选的「模式」是 schema，两者不同',
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

// 各类型支持的「高级参数」白名单与默认值。
//
// 为什么要做成白名单而不是自由键值对：这些参数会改变连接语义，
// 后端也只接受白名单里的键（adapter.mongoAllowedOptions）。
// 前端给出候选，用户不用去翻驱动文档；后端仍然独立校验，不信任前端。
const advancedOptions: Record<
  DatasourceType,
  { key: string; label: string; placeholder: string; options?: { value: string; label: string }[] }[]
> = {
  mongodb: [
    {
      key: 'replicaSet',
      label: '副本集名称（replicaSet）',
      placeholder: '例如 rs0；单节点可留空',
    },
    {
      key: 'readPreference',
      label: '读偏好（readPreference）',
      placeholder: '默认 primary',
      options: [
        { value: 'primary', label: 'primary（只读主）' },
        { value: 'primaryPreferred', label: 'primaryPreferred（主优先）' },
        { value: 'secondary', label: 'secondary（只读从）' },
        { value: 'secondaryPreferred', label: 'secondaryPreferred（从优先）' },
        { value: 'nearest', label: 'nearest（最近节点）' },
      ],
    },
    {
      key: 'tls',
      label: '启用 TLS（tls）',
      placeholder: 'true / false',
      options: [
        { value: 'true', label: 'true（云实例通常需要）' },
        { value: 'false', label: 'false' },
      ],
    },
    {
      key: 'tlsCAFile',
      label: 'CA 证书路径（tlsCAFile）',
      placeholder: '服务器上的绝对路径，如 /etc/ssl/mongo-ca.pem',
    },
    {
      key: 'authMechanism',
      label: '认证机制（authMechanism）',
      placeholder: '默认 SCRAM-SHA-256',
      options: [
        { value: 'SCRAM-SHA-256', label: 'SCRAM-SHA-256' },
        { value: 'SCRAM-SHA-1', label: 'SCRAM-SHA-1（老版本实例）' },
        { value: 'MONGODB-X509', label: 'MONGODB-X509' },
      ],
    },
  ],
  postgresql: [
    {
      key: 'sslmode',
      label: 'SSL 模式（sslmode）',
      placeholder: '默认 disable',
      options: [
        { value: 'disable', label: 'disable（内网直连）' },
        { value: 'require', label: 'require（云实例必须）' },
        { value: 'verify-ca', label: 'verify-ca' },
        { value: 'verify-full', label: 'verify-full' },
      ],
    },
  ],
  mysql: [
    {
      key: 'tls',
      label: '启用 TLS（tls）',
      placeholder: 'true / false',
      options: [
        { value: 'true', label: 'true' },
        { value: 'false', label: 'false' },
      ],
    },
  ],
  redis: [],
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
  const advancedFields = currentType ? advancedOptions[currentType] : []

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
      // 认证库留空表示用默认的 admin（与 mongosh 一致）
      auth_source: '',
      options: {},
      apply_enabled: false,
      max_apply_days: 7,
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
      // 认证库要一起带进表单，否则编辑时会被空值覆盖掉。
      auth_source: record.auth_source ?? '',
      options: record.options ?? {},
      // 先留空，随后用详情接口拿到的明文填进去。
      // 列表接口不回显密码（一次带走全部凭据），只有详情接口返回。
      password: undefined,
      apply_enabled: record.apply_enabled,
      max_apply_days: record.max_apply_days,
      description: record.description,
    })
    setModalOpen(true)
    // 详情接口返回明文密码，直接填进密码框，省去管理员去翻密码本的步骤。
    // 用 setFieldValue 单独写而不是塞进上面的 setFieldsValue：
    // 它依赖一次异步请求，晚于弹窗打开，不能和初始值一起写。
    if (record.has_password) {
      getDatasource(record.id)
        .then((d) => form.setFieldValue('password', d.password ?? ''))
        .catch(() => {
          // 取不到就保持空，按钮旁的提示会说明「留空表示不修改」
        })
    }
  }

  const submit = async () => {
    try {
      const values = await form.validateFields()
      // 不再提交 apply_scopes：可申请范围由数据源整体决定，
      // 登记时不填 = 该数据源下的库和表都可以申请。
      const input: DatasourceInput = {
        ...values,
        // 空的高级参数不提交：后端会按「未设置」处理，避免存一堆空字符串
        options: cleanOptions(values.options) ?? {},
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
        // MongoDB 必须把认证库一起送过去：后端用的是表单里的值，
        // 不传就会退回默认的 admin，测出来的结果和保存后不一致。
        auth_source: form.getFieldValue('auth_source') || undefined,
        // 高级参数也一样：测试连接和正式查询必须是同一套连接条件，
        // 否则会出现「测试通过但查询连不上」这种最难查的错。
        options: cleanOptions(form.getFieldValue('options')),
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
      width: 180,
      render: (_, r) =>
        r.apply_enabled ? (
          <Space direction="vertical" size={2}>
            <Text style={{ fontSize: 12 }}>全部库表</Text>
            <Text type="secondary" style={{ fontSize: 12 }}>
              有该数据源的授权即可访问
            </Text>
          </Space>
        ) : (
          <Text type="secondary">—</Text>
        ),
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
        // 正常路径下开发者已经进不来这个页面（侧边栏隐藏 + 路由拦截），
        // 这里只是兜底：权限是后加的，可能有人收藏了旧链接。
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="数据源管理不开放给开发者"
          description={
            <span>
              数据源的登记、编辑、删除由 DBA 负责。开发者需要连接库时，请到
              <Text strong>「申请授权」</Text>
              页提交申请，由 DBA 审批后获得对应库表的只读授权。
            </span>
          }
        />
      ) : null}

      {isDBA ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 16 }}
          message="密码以明文保存"
          description={
            <span>
              各数据源的连接密码以明文存入平台元数据库，接口只回显掩码（如{' '}
              <Text code>s3cr******</Text>），不返回明文，但任何能读取
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
                  // 切换类型时必须清掉「语义已经变了」的字段：
                  //   database —— MySQL 里是库名，Redis 里是 db 号。
                  //     从 MySQL(shop) 切到 Redis 会残留 "shop"，测试连接报
                  //     「db 号必须是整数」，而界面上看不出这个值是哪来的。
                  //   username —— Redis 通常是密码认证，残留账号会被拿去 AUTH 失败。
                  //   auth_source —— 只有 MongoDB 用。
                  // 之前只清了 username 和 auth_source，database 一直没清。
                  form.setFieldValue('database', '')
                  form.setFieldValue('auth_source', '')
                  // 高级参数不能整体清空。
                  //
                  // 之前的写法是 form.setFieldValue('options', {})，这会带来两个问题：
                  //   1. 编辑已有数据源时，用户只是想改端口，碰一下「类型」下拉
                  //      （哪怕又选回原类型）就把已保存的 replicaSet / sslmode 全删了，
                  //      保存后数据源静默失效
                  //   2. Collapse 里的 Form.Item 是嵌套字段 ['options', key]，
                  //      整体置空后 UI 上也不会提示「这些值被丢了」
                  //
                  // 正确做法是只删掉「新类型不支持」的键：后端对不支持的键会
                  // 直接报错，所以必须清理；但属于两类型共有的键（如 tls）应当保留。
                  const allowedKeys = new Set(advancedOptions[v].map((f) => f.key))
                  const prev = (form.getFieldValue('options') ?? {}) as Record<string, string>
                  const kept: Record<string, string> = {}
                  for (const [k, val] of Object.entries(prev)) {
                    if (allowedKeys.has(k) && val !== undefined && val !== '') kept[k] = val
                  }
                  form.setFieldValue('options', kept)
                  if (!usernamePolicy[v].required) {
                    form.setFieldValue('username', '')
                  }
                }}
              />
            </Form.Item>
          </Space>

          {/* 连接信息分两行排：
              「主机 + 端口」一行，「数据库名」独占下一行。
              原来三个字段挤在同一行，而这一行的各个 Form.Item 宽度
              都是自动计算的：数据库名的 label 和 extra 提示都长，
              label 可能被折成两行，栏位宽度也被撑开，
              结果主机/端口那一栏比数据库名低了一截，看起来像错位。
              拆行后每行的顶边都在同一条基线上，也不再依赖
              label 是否折行这种不可控因素。 */}
          <Space size="middle" style={{ display: 'flex', alignItems: 'flex-start' }}>
            <Form.Item
              name="host"
              label="主机"
              rules={[{ required: true, message: '请输入主机' }]}
              extra={
                currentType === 'mongodb'
                  ? '副本集可填多个成员，用逗号分隔；IPv6 需写成 [::1]'
                  : ''
              }
              style={{ flex: 1 }}
            >
              <Input
                placeholder={
                  currentType === 'mongodb' ? '10.0.0.5 或 10.0.0.5,10.0.0.6' : '10.0.0.5'
                }
              />
            </Form.Item>
            <Form.Item
              name="port"
              label="端口"
              rules={[{ required: true, message: '请输入端口' }]}
              style={{ width: 120 }}
            >
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
          </Space>

          {/* 数据库名独占一行：label 和 extra 提示都比较长，
              放在「主机/端口」旁边时会把两栏挤成不等高、看起来错位。
              这里沿用账号/密码那一行的排版方式（左对齐、顶部对齐）。 */}
          <Form.Item
            name="database"
            label={dbPolicy.label}
            // hint 之前定义了却没渲染，是死字段。这里接上 extra，
            // 让「PG 下填的是 database 名、不是 schema」这句话能真正显示出来。
            extra={dbPolicy.hint || undefined}
            rules={
              dbPolicy.required
                ? [
                    {
                      required: true,
                      message:
                        currentType === 'mongodb'
                          ? 'MongoDB 必须填写默认库，否则无法确定要查哪个库'
                          : 'PostgreSQL 必须填写数据库名',
                    },
                  ]
                : []
            }
          >
            <Input placeholder={dbPolicy.placeholder} />
          </Form.Item>

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
                  ? '已回显当前密码，可直接修改；清空则保留原密码'
                  : '密码以明文存入平台库，请使用只读账号'
              }
              style={{ flex: 1 }}
            >
              <Input.Password
                placeholder="请输入密码"
                // 关掉浏览器的自动填充/自动保存提示：
                // 密码是直接从接口回显的，浏览器若再介入可能覆盖或提示保存。
                autoComplete="off"
              />
            </Form.Item>
          </Space>

          {/* 认证库只对 MongoDB 有意义：账号归属于某个库，
              用错库会在服务端表现成「认证失败」，而密码其实是对的。
              其他类型不渲染，避免多出一个看不懂的字段。 */}
          {currentType === 'mongodb' ? (
            <Form.Item
              name="auth_source"
              label="认证库（authSource）"
              extra="留空按 admin 认证（与 mongosh 一致）。账号建在业务库的专属用户，请填该库名。"
              style={{ maxWidth: 360 }}
            >
              <Input placeholder="留空 = admin" autoComplete="off" />
            </Form.Item>
          ) : null}

          {/* 高级参数默认折叠：只在连不上云实例或副本集时才需要动，
              常驻会让人误以为必填。 */}
          {advancedFields.length > 0 ? (
            <Collapse
              ghost
              style={{ marginBottom: 8 }}
              items={[
                {
                  key: 'advanced',
                  label: (
                    <Text style={{ fontSize: 13 }}>
                      高级连接参数
                      <Text type="secondary" style={{ fontSize: 12, marginLeft: 8 }}>
                        云实例 / 副本集 / TLS 通常需要
                      </Text>
                    </Text>
                  ),
                  children: (
                    <Space direction="vertical" style={{ width: '100%' }}>
                      {advancedFields.map((f) => (
                        <Form.Item
                          key={f.key}
                          name={['options', f.key]}
                          label={f.label}
                          style={{ marginBottom: 12 }}
                        >
                          {f.options ? (
                            <Select
                              allowClear
                              placeholder={f.placeholder}
                              options={f.options}
                              style={{ maxWidth: 340 }}
                            />
                          ) : (
                            <Input placeholder={f.placeholder} style={{ maxWidth: 340 }} />
                          )}
                        </Form.Item>
                      ))}
                    </Space>
                  ),
                },
              ]}
            />
          ) : null}

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
            <Input.TextArea rows={2} placeholder="给开发者看的说明，会显示在「申请授权」页" />
          </Form.Item>

          <Divider orientation="left" plain>
            可申请配置
          </Divider>

          <Space size="large" align="start">
            <Form.Item
              name="apply_enabled"
              label="开放申请"
              valuePropName="checked"
              extra="关闭后开发者无法在「申请授权」页看到该数据源"
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

          {/* 范围不再需要填写：授权粒度是「数据源 + 库」，开放申请即可申请
              其下任意库表。这里明确写出来，避免管理员以为漏填了配置项。 */}
          <Alert
            type="info"
            showIcon
            message="可申请范围：全部库表"
            description="开放申请后，开发者即可申请该数据源下的任意库；拿到授权就能查询该库下的所有表，无需逐库逐表配置。"
          />
        </Form>
      </Modal>
    </Card>
  )
}

