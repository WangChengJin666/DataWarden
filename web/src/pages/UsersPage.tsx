import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Button,
  Card,
  Col,
  Form,
  Input,
  message,
  Modal,
  Popconfirm,
  Row,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { KeyOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import {
  createUser,
  deleteUser,
  getCurrentUser,
  listGroups,
  listUsers,
  resetUserPassword,
  setUserStatus,
  updateUser,
} from '@/api/client'
import type { Role, UserGroup, UserStatus, UserView } from '@/api/types'
import PasswordOnceModal from '@/components/PasswordOnceModal'

const { Text } = Typography

const roleLabels: Record<Role, { label: string; color: string }> = {
  developer: { label: '开发者', color: 'default' },
  dba: { label: 'DBA', color: 'blue' },
  superadmin: { label: '超级管理员', color: 'red' },
}

const statusLabels: Record<UserStatus, { label: string; color: string }> = {
  active: { label: '在用', color: 'green' },
  disabled: { label: '已停用', color: 'orange' },
  deleted: { label: '已删除', color: 'default' },
}

/** 未分组的选项值。用 0 而不是 undefined，方便 Select 受控。后端会把 0 归一成 NULL。 */
const UNGROUPED = 0

type StatusFilter = 'default' | UserStatus

interface UserFormValues {
  username: string
  email: string
  role: Role
  group_id: number
  status: 'active' | 'disabled'
}

/**
 * 用户管理。仅超级管理员可见（路由与后端接口是同一层保护）。
 *
 * 几个刻意的取舍：
 *   - 当前登录者自己那一行禁用「停用 / 删除」：这些操作会把自己锁在门外。
 *     后端也会拦（can disable/delete your own account），UI 提前置灰，
 *     避免点一下才报错。
 *   - 删除是逻辑删除：人留在库里、历史授权与审批记录都在，用户名被释放。
 *     确认框必须讲清这点，否则管理员会以为这是他理解的「删库」。
 *   - 已删除的账号只能看，不能改：所有操作按钮置灰。
 */
export default function UsersPage() {
  const me = getCurrentUser()
  const [items, setItems] = useState<UserView[]>([])
  const [groups, setGroups] = useState<UserGroup[]>([])
  const [loading, setLoading] = useState(false)

  const [keyword, setKeyword] = useState('')
  // debouncedKeyword 才是真正进请求的值：直接拿 keyword 做 load 的依赖，
  // 每敲一个字符就发一次请求，输入六个字打六次库。
  const [debouncedKeyword, setDebouncedKeyword] = useState('')
  const [roleFilter, setRoleFilter] = useState<Role | ''>('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('default')
  const [groupFilter, setGroupFilter] = useState<number | undefined>(undefined)

  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<UserView | null>(null)
  const [saving, setSaving] = useState(false)
  const [form] = Form.useForm<UserFormValues>()

  const [issued, setIssued] = useState<{
    password: string
    username: string
    mode: 'created' | 'reset'
  } | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setItems(
        await listUsers({
          keyword: debouncedKeyword,
          role: roleFilter,
          // 默认口径交给后端（active + disabled），这里只显式传时需要
          status: statusFilter === 'default' ? undefined : [statusFilter],
          group_id: groupFilter === UNGROUPED ? null : groupFilter,
          ungrouped: groupFilter === UNGROUPED,
        }),
      )
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [debouncedKeyword, roleFilter, statusFilter, groupFilter])

  // 关键词输入防抖 300ms。其余筛选（角色/状态/组）是下拉选择，
  // 一次点击就是一次完整意图，不需要防抖。
  useEffect(() => {
    const timer = setTimeout(() => setDebouncedKeyword(keyword), 300)
    return () => clearTimeout(timer)
  }, [keyword])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    listGroups()
      .then(setGroups)
      .catch((e: Error) => message.error(e.message))
  }, [])

  const openCreate = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ role: 'developer', group_id: UNGROUPED, status: 'active', email: '' })
    setOpen(true)
  }

  const openEdit = (u: UserView) => {
    setEditing(u)
    form.setFieldsValue({
      username: u.username,
      email: u.email,
      role: u.role,
      group_id: u.group_id ?? UNGROUPED,
      status: u.status as 'active' | 'disabled',
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    setSaving(true)
    try {
      if (editing) {
        await updateUser(editing.id, {
          email: v.email,
          role: v.role,
          group_id: v.group_id === UNGROUPED ? null : v.group_id,
        })
        message.success('已保存')
        setOpen(false)
        void load()
      } else {
        const res = await createUser({
          username: v.username,
          email: v.email,
          role: v.role,
          status: v.status,
          group_id: v.group_id === UNGROUPED ? null : v.group_id,
        })
        setOpen(false)
        setIssued({ password: res.password, username: v.username, mode: 'created' })
        void load()
      }
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const toggleStatus = async (u: UserView) => {
    // status 可能是 deleted，但已删除行的按钮已置灰，这里收敛成两值
    const next = u.status === 'active' ? 'disabled' : 'active'
    try {
      await setUserStatus(u.id, next)
      message.success(next === 'disabled' ? '已停用，权限立即失效' : '已启用')
      void load()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const doReset = async (u: UserView) => {
    try {
      const pw = await resetUserPassword(u.id)
      setIssued({ password: pw, username: u.username, mode: 'reset' })
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const remove = async (u: UserView) => {
    try {
      await deleteUser(u.id)
      message.success(`已删除「${u.username}」，用户名已释放`)
      void load()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const groupOptions = useMemo(
    () => [
      { label: '未分组', value: UNGROUPED },
      ...groups.map((g) => ({ label: g.name, value: g.id })),
    ],
    [groups],
  )

  const columns: ColumnsType<UserView> = [
    {
      title: '用户名',
      dataIndex: 'username',
      key: 'username',
      render: (v: string, u) => (
        <Space size={4}>
          <Text>{v}</Text>
          {u.username === me?.username && u.status !== 'deleted' && (
            <Tag color="blue" style={{ fontSize: 11 }}>
              当前账号
            </Tag>
          )}
          {/* 已删除账号的用户名带 #deleted#<id> 后缀，单列 ID 便于与审计日志对应 */}
          {u.status === 'deleted' && (
            <Tag color="default" style={{ fontSize: 11 }}>
              ID {u.id}
            </Tag>
          )}
        </Space>
      ),
    },
    {
      title: '邮箱',
      dataIndex: 'email',
      key: 'email',
      render: (v: string) => v || <Text type="secondary">—</Text>,
    },
    {
      title: '角色',
      dataIndex: 'role',
      key: 'role',
      width: 120,
      render: (v: Role) => (
        <Tag color={roleLabels[v]?.color}>{roleLabels[v]?.label ?? v}</Tag>
      ),
    },
    {
      title: '所属组',
      dataIndex: 'group_name',
      key: 'group_name',
      width: 140,
      render: (v: string) => v || <Text type="secondary">未分组</Text>,
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      width: 100,
      render: (v: UserStatus) => (
        <Tag color={statusLabels[v]?.color}>{statusLabels[v]?.label ?? v}</Tag>
      ),
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      key: 'created_at',
      width: 170,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '操作',
      key: 'actions',
      width: 280,
      render: (_, u) => {
        const isDeleted = u.status === 'deleted'
        // 用用户名判断「是不是自己」：登录响应里只有 token 与 role，没有用户 id
        //（见 api/types.ts 的 LoginResponse），前端拿不到自己的 id。
        // 这样判断不会误伤：已删除账号的用户名带 #deleted#<id> 后缀，
        // 永远不会等于任何人的登录名，所以「同名复用给新账号」时
        // 旧行不会把新账号标成自己。
        // 真正的保护在后端：Delete 与 SetStatus 都会拒绝操作当前登录者。
        const isSelf = u.username === me?.username
        return (
          <Space size="small" wrap>
            <Button type="link" size="small" disabled={isDeleted} onClick={() => openEdit(u)}>
              编辑
            </Button>
            <Popconfirm
              title="重置该用户密码？"
              description="新密码只显示一次，旧密码立即失效。"
              okText="重置"
              cancelText="取消"
              disabled={isDeleted}
              onConfirm={() => doReset(u)}
            >
              <Button type="link" size="small" icon={<KeyOutlined />} disabled={isDeleted}>
                重置密码
              </Button>
            </Popconfirm>
            <Button
              type="link"
              size="small"
              danger={u.status === 'active'}
              disabled={isDeleted || isSelf}
              onClick={() => toggleStatus(u)}
            >
              {u.status === 'active' ? '停用' : '启用'}
            </Button>
            <Popconfirm
              title={`删除「${u.username}」？`}
              description={
                <div style={{ maxWidth: 320 }}>
                  该账号将被停用并从列表中移除（逻辑删除）。历史授权与审批记录全部保留，
                  用户名会改写为{' '}
                  <Text code>
                    {u.username}#deleted#{u.id}
                  </Text>
                  ，原名可立即分配给新账号。此操作不可恢复，如需保留可恢复性请改用「停用」。
                </div>
              }
              okText="删除"
              cancelText="取消"
              okButtonProps={{ danger: true }}
              disabled={isDeleted || isSelf}
              onConfirm={() => remove(u)}
            >
              <Button type="link" size="small" danger disabled={isDeleted || isSelf}>
                删除
              </Button>
            </Popconfirm>
          </Space>
        )
      },
    },
  ]

  return (
    <Card
      title="用户管理"
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} onClick={load}>
            刷新
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新建用户
          </Button>
        </Space>
      }
    >
      <Row gutter={12} style={{ marginBottom: 16 }}>
        <Col flex="220px">
          <Input.Search
            allowClear
            placeholder="搜索用户名或邮箱"
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onSearch={() => void load()}
          />
        </Col>
        <Col flex="150px">
          <Select
            allowClear
            style={{ width: '100%' }}
            placeholder="全部角色"
            value={roleFilter || undefined}
            onChange={(v) => setRoleFilter((v as Role) ?? '')}
            options={Object.entries(roleLabels).map(([value, cfg]) => ({
              value,
              label: cfg.label,
            }))}
          />
        </Col>
        <Col flex="170px">
          <Select
            style={{ width: '100%' }}
            value={statusFilter}
            onChange={(v) => setStatusFilter(v)}
            options={[
              { value: 'default', label: '在用 + 已停用' },
              { value: 'active', label: '仅在用' },
              { value: 'disabled', label: '仅已停用' },
              { value: 'deleted', label: '已删除' },
            ]}
          />
        </Col>
        <Col flex="170px">
          <Select
            allowClear
            style={{ width: '100%' }}
            placeholder="全部组"
            value={groupFilter}
            onChange={(v) => setGroupFilter(v)}
            options={groupOptions}
          />
        </Col>
      </Row>

      <Table
        rowKey="id"
        size="middle"
        loading={loading}
        columns={columns}
        dataSource={items}
        pagination={{ pageSize: 20, showSizeChanger: false }}
      />

      <Modal
        title={editing ? '编辑用户' : '新建用户'}
        open={open}
        onCancel={() => setOpen(false)}
        onOk={submit}
        confirmLoading={saving}
        okText={editing ? '保存' : '创建'}
        destroyOnClose
      >
        <Form form={form} layout="vertical" preserve={false}>
          <Form.Item
            name="username"
            label="用户名"
            rules={[
              { required: true, message: '请填写用户名' },
              { pattern: /^[A-Za-z0-9_.-]+$/, message: '只能包含字母、数字与 _ - .' },
              { max: 64, message: '不能超过 64 个字符' },
            ]}
          >
            {/* 用户名不可改：它是登录凭据的一部分，历史审计里的 actor_name 已按当时名字落库 */}
            <Input placeholder="例如：zhangsan" disabled={!!editing} />
          </Form.Item>
          <Form.Item name="email" label="邮箱">
            <Input placeholder="选填" />
          </Form.Item>
          <Row gutter={12}>
            <Col span={12}>
              <Form.Item name="role" label="角色" rules={[{ required: true }]}>
                <Select
                  // 编辑自己时禁用角色下拉：把自己降级会立刻失去账号管理入口，
                  // 后端会以 409 拒绝，这里提前锁住避免无谓报错。
                  disabled={!!editing && editing.username === me?.username}
                  options={Object.entries(roleLabels).map(([value, cfg]) => ({
                    value,
                    label: cfg.label,
                  }))}
                />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="group_id" label="所属组">
                <Select options={groupOptions} />
              </Form.Item>
            </Col>
          </Row>
          {!editing && (
            <>
              <Form.Item name="status" label="初始状态">
                <Select
                  options={[
                    { value: 'active', label: '在用' },
                    { value: 'disabled', label: '已停用（创建后不能登录）' },
                  ]}
                />
              </Form.Item>
              <Text type="secondary" style={{ fontSize: 12 }}>
                初始密码由系统随机生成，创建成功后只显示一次。
              </Text>
            </>
          )}
        </Form>
      </Modal>

      <PasswordOnceModal
        password={issued?.password ?? null}
        username={issued?.username ?? ''}
        mode={issued?.mode ?? 'created'}
        onClose={() => setIssued(null)}
      />
    </Card>
  )
}
