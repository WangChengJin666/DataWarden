import { useEffect, useState } from 'react'
import {
  Button,
  Card,
  Input,
  message,
  Modal,
  Segmented,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import {
  approveRequest,
  getCurrentUser,
  listRequests,
  rejectRequest,
} from '@/api/client'
import type { GrantRequest, GrantRequestStatus } from '@/api/types'

const { Text } = Typography

const statusMeta: Record<GrantRequestStatus, { color: string; label: string }> = {
  pending: { color: 'processing', label: '待审批' },
  approved: { color: 'success', label: '已通过' },
  rejected: { color: 'error', label: '已驳回' },
  canceled: { color: 'default', label: '已撤销' },
}

export default function ReviewPage() {
  const [status, setStatus] = useState<string>('pending')
  const [items, setItems] = useState<GrantRequest[]>([])
  const [loading, setLoading] = useState(false)
  const [target, setTarget] = useState<{ req: GrantRequest; action: 'approve' | 'reject' } | null>(null)
  const [comment, setComment] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const me = getCurrentUser()
  const isDBA = ['dba', 'superadmin'].includes(me?.role ?? '')

  const load = async () => {
    setLoading(true)
    try {
      setItems(await listRequests(status))
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (isDBA) void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status])

  const submit = async () => {
    if (!target) return
    if (target.action === 'reject' && !comment.trim()) {
      message.warning('驳回必须填写原因')
      return
    }
    setSubmitting(true)
    try {
      if (target.action === 'approve') {
        await approveRequest(target.req.id, comment)
        message.success('已通过，授权立即生效')
      } else {
        await rejectRequest(target.req.id, comment)
        message.success('已驳回')
      }
      setTarget(null)
      setComment('')
      void load()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const columns: ColumnsType<GrantRequest> = [
    { title: 'ID', dataIndex: 'id', width: 70 },
    { title: '申请人', dataIndex: 'username', width: 90 },
    { title: '数据源', dataIndex: 'datasource_name', width: 120 },
    {
      title: '申请范围',
      dataIndex: 'scope',
      width: 150,
      render: (v: string) => <Text code>{v}</Text>,
    },
    { title: '天数', dataIndex: 'days', width: 60 },
    {
      title: '理由',
      dataIndex: 'reason',
      width: 200,
      ellipsis: true,
      render: (v: string) => <Text style={{ fontSize: 13 }}>{v}</Text>,
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (v: GrantRequestStatus) => (
        <Tag color={statusMeta[v].color}>{statusMeta[v].label}</Tag>
      ),
    },
    {
      title: '提交时间',
      dataIndex: 'created_at',
      width: 140,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '操作',
      width: 140,
      fixed: 'right',
      render: (_, r) => {
        if (r.status !== 'pending') {
          return <Text type="secondary">{r.review_comment || '—'}</Text>
        }
        // 自审拦截：前端先挡一道，后端也有校验
        if (r.user_id === undefined) return null
        const isSelf = r.username === me?.username
        if (isSelf) {
          return <Text type="warning">不能审批自己的申请</Text>
        }
        return (
          <Space>
            <Button
              size="small"
              type="primary"
              onClick={() => {
                setTarget({ req: r, action: 'approve' })
                setComment('')
              }}
            >
              通过
            </Button>
            <Button
              size="small"
              danger
              onClick={() => {
                setTarget({ req: r, action: 'reject' })
                setComment('')
              }}
            >
              驳回
            </Button>
          </Space>
        )
      },
    },
  ]

  if (!isDBA) {
    return (
      <Card title="授权审批">
        <Text type="secondary">仅 DBA 或超级管理员可访问此页面。</Text>
      </Card>
    )
  }

  return (
    <Card
      title="授权审批"
      extra={
        <Segmented
          value={status}
          onChange={(v) => setStatus(String(v))}
          options={[
            { label: '待审批', value: 'pending' },
            { label: '已通过', value: 'approved' },
            { label: '已驳回', value: 'rejected' },
            { label: '已撤销', value: 'canceled' },
            { label: '全部', value: '' },
          ]}
        />
      }
    >
      <Table
        rowKey="id"
        columns={columns}
        dataSource={items}
        loading={loading}
        scroll={{ x: 1180 }}
        pagination={{ pageSize: 20 }}
      />

      <Modal
        title={target?.action === 'approve' ? '通过申请' : '驳回申请'}
        open={!!target}
        onCancel={() => setTarget(null)}
        onOk={submit}
        confirmLoading={submitting}
        okText={target?.action === 'approve' ? '确认通过' : '确认驳回'}
        okButtonProps={{ danger: target?.action === 'reject' }}
      >
        {target ? (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Text>
              申请人 <Text strong>{target.req.username}</Text> 申请
              <Text strong> {target.req.datasource_name} </Text>
              的 <Text code>{target.req.scope}</Text> 查询权限，共 {target.req.days} 天。
            </Text>
            <Text type="secondary">理由：{target.req.reason}</Text>
            <Input.TextArea
              rows={3}
              placeholder={target.action === 'reject' ? '请填写驳回原因（必填）' : '审批意见（可选）'}
              value={comment}
              onChange={(e) => setComment(e.target.value)}
            />
          </Space>
        ) : null}
      </Modal>
    </Card>
  )
}
