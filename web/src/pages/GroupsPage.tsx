import { useEffect, useState } from 'react'
import {
  Button,
  Card,
  Form,
  Input,
  message,
  Modal,
  Popconfirm,
  Space,
  Table,
  Typography,
} from 'antd'
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import { createGroup, deleteGroup, listGroups, updateGroup } from '@/api/client'
import type { UserGroup } from '@/api/types'

const { Text } = Typography

/**
 * 组管理。语义等同部门管理：用户归属某个组，组只做归类，不带任何数据权限。
 *
 * 删组的口径是「人留着、只是不再属于任何组」——这一点必须写在确认框里，
 * 否则管理员会以为删部门等同于把人删了，不敢动。
 */
export default function GroupsPage() {
  const [items, setItems] = useState<UserGroup[]>([])
  const [loading, setLoading] = useState(false)
  const [editing, setEditing] = useState<UserGroup | null>(null)
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [form] = Form.useForm<{ name: string; description: string }>()

  const load = async () => {
    setLoading(true)
    try {
      setItems(await listGroups())
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  // destroyOnClose 让 Form 只在弹窗打开时挂载，因此在 setOpen(true) 之前
  // setFieldsValue 会把值丢掉（编辑页空白）。回填统一放到 afterOpenChange。
  const openCreate = () => {
    setEditing(null)
    setOpen(true)
  }

  const openEdit = (g: UserGroup) => {
    setEditing(g)
    setOpen(true)
  }

  const fillForm = (g: UserGroup | null) => {
    form.setFieldsValue(
      g ? { name: g.name, description: g.description } : { name: '', description: '' },
    )
  }

  const submit = async () => {
    const values = await form.validateFields()
    setSaving(true)
    try {
      if (editing) {
        await updateGroup(editing.id, values)
        message.success('已更新')
      } else {
        await createGroup(values)
        message.success('已创建')
      }
      setOpen(false)
      void load()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const remove = async (g: UserGroup) => {
    try {
      await deleteGroup(g.id)
      message.success(
        g.member_count > 0
          ? `已删除「${g.name}」，${g.member_count} 名成员已转为未分组`
          : `已删除「${g.name}」`,
      )
      void load()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const columns: ColumnsType<UserGroup> = [
    { title: '组名', dataIndex: 'name', key: 'name' },
    {
      title: '说明',
      dataIndex: 'description',
      key: 'description',
      render: (v: string) => v || <Text type="secondary">—</Text>,
    },
    {
      title: '成员数',
      dataIndex: 'member_count',
      key: 'member_count',
      width: 100,
      // 成员数只统计未删除账号，与用户页的默认口径一致
      render: (v: number) => (v > 0 ? `${v} 人` : <Text type="secondary">0</Text>),
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      key: 'created_at',
      width: 180,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '操作',
      key: 'actions',
      width: 160,
      render: (_, g) => (
        <Space size="small">
          <Button type="link" size="small" onClick={() => openEdit(g)}>
            编辑
          </Button>
          <Popconfirm
            title={`删除「${g.name}」？`}
            description={
              g.member_count > 0
                ? `组内 ${g.member_count} 名成员不会被删除，只会变成「未分组」。`
                : '该组没有成员。'
            }
            okText="删除"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            onConfirm={() => remove(g)}
          >
            <Button type="link" size="small" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <Card
      title="组管理"
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} onClick={load}>
            刷新
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新建组
          </Button>
        </Space>
      }
    >
      <Text type="secondary" style={{ display: 'block', marginBottom: 16 }}>
        组用于归类用户（类似部门），不影响数据权限。删除组不会删除其中的用户。
      </Text>
      <Table
        rowKey="id"
        size="middle"
        loading={loading}
        columns={columns}
        dataSource={items}
        pagination={false}
      />

      <Modal
        title={editing ? '编辑组' : '新建组'}
        open={open}
        onCancel={() => setOpen(false)}
        onOk={submit}
        confirmLoading={saving}
        okText={editing ? '保存' : '创建'}
        destroyOnClose
        afterOpenChange={(visible) => {
          if (visible) fillForm(editing)
        }}
      >
        <Form form={form} layout="vertical" preserve={false}>
          <Form.Item
            name="name"
            label="组名"
            rules={[
              { required: true, whitespace: true, message: '请填写组名' },
              // 用 validator 而不是 max:64：长度要按「去掉首尾空白后的字符数」
              // 来算，否则输入 64 个字符再带两个尾随空格就会被前端放行、
              // 后端拒绝，用户看到的是「明明没超长却说超长」。
              // transform 让 Form 在校验前统一 trim，与后端 TrimSpace 口径一致。
              {
                transform: (v: string) => (v ?? '').trim(),
                validator: (_, v: string) =>
                  (v ?? '').length > 64
                    ? Promise.reject(new Error('组名不能超过 64 个字符'))
                    : Promise.resolve(),
              },
            ]}
          >
            <Input placeholder="例如：数据平台组" maxLength={64} />
          </Form.Item>
          <Form.Item name="description" label="说明">
            <Input.TextArea rows={3} placeholder="选填，一句话说明这个组负责什么" />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  )
}
