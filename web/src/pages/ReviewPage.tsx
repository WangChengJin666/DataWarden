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
import { isAllDatabasesScope } from '@/api/scope'
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
      title: '授权范围',
      dataIndex: 'scope',
      width: 150,
      // 申请粒度是数据源，审批通过的授权固定落 "*"（整个数据源）。
      // 直接显示 * 会被读成「通配符/全部数据源」，所以翻成人话；
      // 历史单据里可能还有库级 scope，按原文展示便于对照。
      render: (v: string) =>
        isAllDatabasesScope(v) ? <Tag color="blue">整个数据源</Tag> : <Text code>{v}</Text>,
    },
    { title: '天数', dataIndex: 'days', width: 60 },
    {
      // 导出权限必须在审批列表里显式可见：DBA 批的是「查询 + 导出」两件事，
      // 而导出意味着数据能落成本地带走，漏看这一列就等于批了一件没看过的事。
      title: '导出',
      dataIndex: 'can_export',
      width: 90,
      render: (v: boolean) =>
        v ? <Tag color="blue">申请导出</Tag> : <Text type="secondary">仅查询</Text>,
    },
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
        scroll={{ x: 1270 }}
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
              {isAllDatabasesScope(target.req.scope) ? (
                <Text strong>整个数据源（所有库和表）</Text>
              ) : (
                <>
                  的 <Text code>{target.req.scope}</Text>
                </>
              )}
              的查询权限，共 {target.req.days} 天。
            </Text>
            {target.req.can_export ? (
              <Text>
                申请人还要求 <Text strong>可导出查询结果</Text>：
                通过后 ta 可以在查询页把结果导出成 Excel 文件带走。
                确认数据可以外带再通过。
              </Text>
            ) : (
              <Text type="secondary">
                申请人未要求导出权限，通过后只能在页面上查看结果，不能导出文件。
              </Text>
            )}
            <Text type="secondary">理由：{target.req.reason}</Text>
            {/* 历史遗留的待审批单可能只申请了单个库（改口径前提交）。
                审批通过时授权一律落成整个数据源，与申请人当初写的不一致，
                所以必须在这里提前讲清，避免 DBA 批完才发现范围变大了。 */}
            {target.action === 'approve' && !isAllDatabasesScope(target.req.scope) ? (
              <Text type="warning">
                该申请提交于授权口径变更之前（原范围 {target.req.scope}）。
                通过后将按现行口径授予<Text strong>整个数据源</Text>的查询权限，
                请确认范围可接受。
              </Text>
            ) : null}
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
