import { useEffect, useState } from 'react'
import {
  Button,
  Card,
  message,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { ReloadOutlined, StopOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import { getCurrentUser, listActiveGrants, revokeGrant } from '@/api/client'
import type { GrantView } from '@/api/types'

const { Text } = Typography

/**
 * 已生效授权管理。
 *
 * 与「授权审批」分开的原因：那一页处理的是申请单（pending），
 * 这一页处理的是已经生效、正在用的授权。要回答「现在谁能查哪个库」
 * 和「谁用了不该有的权限，立刻收回」这两个问题。
 *
 * 之前收回权限只能连数据库改 revoked_at，审批批错了没有任何补救路径，
 * 这对一个审计平台来说是不成立的。
 */
export default function GrantsPage() {
  const [items, setItems] = useState<GrantView[]>([])
  const [loading, setLoading] = useState(false)
  const [revoking, setRevoking] = useState<number | null>(null)

  const isDBA = ['dba', 'superadmin'].includes(getCurrentUser()?.role ?? '')

  const load = async () => {
    setLoading(true)
    try {
      setItems(await listActiveGrants())
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (isDBA) void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const doRevoke = async (id: number) => {
    setRevoking(id)
    try {
      await revokeGrant(id)
      message.success('已收回，权限立即失效')
      void load()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setRevoking(null)
    }
  }

  const columns: ColumnsType<GrantView> = [
    { title: 'ID', dataIndex: 'id', width: 70 },
    { title: '用户', dataIndex: 'username', width: 110 },
    { title: '数据源', dataIndex: 'datasource_name', width: 140 },
    {
      title: '授权范围',
      dataIndex: 'scope',
      width: 220,
      // scope = "*" 表示整个数据源（申请审批通过后落的就是它）。
      // 原样显示一个 * 会被误读成「所有数据源」，所以翻成人话；
      // 改口径前审批的库级授权（"shop:*"）保持原文，便于对照。
      render: (v: string) =>
        v === '*' ? <Tag color="blue">整个数据源（所有库和表）</Tag> : <Text code>{v}</Text>,
    },
    {
      title: '到期时间',
      dataIndex: 'expires_at',
      width: 220,
      render: (v: string) => {
        const exp = dayjs(v)
        const leftHours = exp.diff(dayjs(), 'hour')
        // 剩余不到 24 小时的醒目标出来，方便判断「要不要续」
        const soon = leftHours < 24
        return (
          <Space size={4}>
            <Text style={{ fontSize: 13 }}>{exp.format('YYYY-MM-DD HH:mm')}</Text>
            {soon ? (
              <Tag color="warning" style={{ marginInlineEnd: 0 }}>
                剩 {Math.max(leftHours, 0)} 小时
              </Tag>
            ) : null}
          </Space>
        )
      },
    },
    {
      title: '授予时间',
      dataIndex: 'created_at',
      width: 150,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '操作',
      width: 110,
      fixed: 'right',
      render: (_, r) => (
        <Popconfirm
          title="确认收回该授权？"
          description="收回后该用户立即无法查询，无需等待到期。"
          okText="确认收回"
          okButtonProps={{ danger: true }}
          onConfirm={() => doRevoke(r.id)}
        >
          <Button
            size="small"
            danger
            icon={<StopOutlined />}
            loading={revoking === r.id}
          >
            收回
          </Button>
        </Popconfirm>
      ),
    },
  ]

  if (!isDBA) {
    return (
      <Card title="授权管理">
        <Text type="secondary">仅 DBA 或超级管理员可访问此页面。</Text>
      </Card>
    )
  }

  return (
    <Card
      title="授权管理"
      extra={
        <Button icon={<ReloadOutlined />} onClick={() => void load()}>
          刷新
        </Button>
      }
    >
      <Text type="secondary" style={{ fontSize: 13, display: 'block', marginBottom: 16 }}>
        这里是当前真正生效的授权（未撤销且未过期）。授权到期后由后台定时任务自动失效，
        通常在一分钟内清理完成；需要立即收回时用下面的「收回」按钮。
      </Text>
      <Table
        rowKey="id"
        columns={columns}
        dataSource={items}
        loading={loading}
        scroll={{ x: 1020 }}
        pagination={{ pageSize: 20, showTotal: (t) => `共 ${t} 条` }}
        locale={{ emptyText: '当前没有生效中的授权' }}
      />
    </Card>
  )
}
