import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  InputNumber,
  message,
  Modal,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import {
  cancelRequest,
  createGrantRequest,
  listApplyable,
  listMyRequests,
} from '@/api/client'
import type { Datasource, GrantRequest, GrantRequestStatus } from '@/api/types'

const { Text } = Typography

const statusMeta: Record<GrantRequestStatus, { color: string; label: string }> = {
  pending: { color: 'processing', label: '待审批' },
  approved: { color: 'success', label: '已通过' },
  rejected: { color: 'error', label: '已驳回' },
  canceled: { color: 'default', label: '已撤销' },
}

export default function ApplyPage() {
  const [applyable, setApplyable] = useState<Datasource[]>([])
  const [mine, setMine] = useState<GrantRequest[]>([])
  const [loading, setLoading] = useState(false)
  const [modalOpen, setModalOpen] = useState(false)
  const [selected, setSelected] = useState<Datasource | null>(null)
  const [form] = Form.useForm()

  const load = async () => {
    setLoading(true)
    try {
      const [a, m] = await Promise.all([listApplyable(), listMyRequests()])
      setApplyable(a)
      setMine(m)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const openApply = (ds: Datasource) => {
    setSelected(ds)
    form.resetFields()
    form.setFieldsValue({
      days: Math.min(7, ds.max_apply_days),
    })
    setModalOpen(true)
  }

  const submit = async () => {
    if (!selected) return
    try {
      const values = await form.validateFields()
      await createGrantRequest({
        datasource_id: selected.id,
        days: values.days,
        reason: values.reason,
      })
      message.success('申请已提交，等待 DBA 审批')
      setModalOpen(false)
      void load()
    } catch (e) {
      if ((e as { errorFields?: unknown }).errorFields) return
      message.error((e as Error).message)
    }
  }

  const doCancel = async (id: number) => {
    try {
      await cancelRequest(id)
      message.success('已撤销')
      void load()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const columns: ColumnsType<GrantRequest> = [
    { title: 'ID', dataIndex: 'id', width: 70 },
    { title: '数据源', dataIndex: 'datasource_name', width: 160 },
    { title: '天数', dataIndex: 'days', width: 70 },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (v: GrantRequestStatus) => (
        <Tag color={statusMeta[v].color}>{statusMeta[v].label}</Tag>
      ),
    },
    {
      title: '审批意见',
      dataIndex: 'review_comment',
      render: (v: string) => v || <Text type="secondary">—</Text>,
    },
    {
      title: '提交时间',
      dataIndex: 'created_at',
      width: 150,
      render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:mm'),
    },
    {
      title: '操作',
      width: 90,
      render: (_, r) =>
        r.status === 'pending' ? (
          <a onClick={() => doCancel(r.id)}>撤销</a>
        ) : (
          <Text type="secondary">—</Text>
        ),
    },
  ]

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Card title="可申请的数据源" loading={loading}>
        {applyable.length === 0 ? (
          <Text type="secondary">
            暂无开放申请的数据源。请联系 DBA 在「数据源管理」中开启申请。
          </Text>
        ) : (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            {applyable.map((ds) => (
              <Card key={ds.id} size="small" type="inner">
                <div style={{ display: 'flex', alignItems: 'flex-start', gap: 16 }}>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <Space wrap>
                      <Text strong>{ds.name}</Text>
                      <Tag>{ds.type}</Tag>
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        {ds.host}:{ds.port}
                      </Text>
                    </Space>
                    {ds.description ? (
                      <div style={{ marginTop: 6 }}>
                        <Text type="secondary">{ds.description}</Text>
                      </div>
                    ) : null}
                    <Descriptions size="small" column={1} style={{ marginTop: 8 }}>
                      <Descriptions.Item label="最长可申请">
                        {ds.max_apply_days} 天
                      </Descriptions.Item>
                      <Descriptions.Item label="授权范围">
                        <Text type="secondary">
                          该数据源下的全部库与表（申请时无需选择库）
                        </Text>
                      </Descriptions.Item>
                    </Descriptions>
                  </div>
                  <Button type="primary" onClick={() => openApply(ds)} style={{ flexShrink: 0 }}>
                    申请授权
                  </Button>
                </div>
              </Card>
            ))}
          </Space>
        )}
      </Card>

      <Card title="我的申请">
        <Table
          rowKey="id"
          columns={columns}
          dataSource={mine}
          loading={loading}
          pagination={{ pageSize: 10 }}
        />
      </Card>

      <Modal
        title={`申请授权：${selected?.name ?? ''}`}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        onOk={submit}
        okText="提交申请"
        destroyOnClose
      >
        <Form form={form} layout="vertical" preserve={false}>
          {/* 申请不再选库：一次申请覆盖整个数据源。
              之前这里的「申请库」输入框要求用户记住库名并手打，
              拼错一个字母就得等审批通过后自己发现——而权限模型里
              本来就没有「一库一申请」的必要（审批通过即拥有该数据源
              全部库表），留着只会增加一次出错机会。 */}
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message="审批通过后，可查询该数据源下所有库和表"
            description="无需逐库或逐表申请，也无需填写库名。"
          />
          <Form.Item
            name="days"
            label="申请天数"
            rules={[{ required: true, message: '请填写天数' }]}
            extra={`最长 ${selected?.max_apply_days ?? 0} 天`}
          >
            <InputNumber min={1} max={selected?.max_apply_days ?? 1} addonAfter="天" style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item
            name="reason"
            label="申请理由"
            rules={[{ required: true, message: '请填写理由' }]}
            extra="审批时 DBA 会看到这段说明"
          >
            <Input.TextArea rows={3} placeholder="例如：排查订单超时问题，需要查询近 3 天数据" />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
