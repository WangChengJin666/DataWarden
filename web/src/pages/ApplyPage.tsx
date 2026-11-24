import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
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

  // 可申请数据源用表格分页呈现。之前一个数据源一张小卡片，卡片里还有两块
  // Descriptions，稍有十几个数据源就把「我的申请」挤到屏幕外，要点申请还得
  // 来回滚动找。表格把连接信息和最长天数收进列里，一屏能看一页。
  const applyColumns: ColumnsType<Datasource> = [
    {
      title: '数据源',
      dataIndex: 'name',
      width: 200,
      render: (v: string, r) => (
        <Space direction="vertical" size={0}>
          <Text strong>{v}</Text>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {r.host}:{r.port}
          </Text>
        </Space>
      ),
    },
    {
      title: '类型',
      dataIndex: 'type',
      width: 110,
      render: (v: string) => <Tag>{v}</Tag>,
    },
    {
      title: '说明',
      dataIndex: 'description',
      render: (v: string) => v || <Text type="secondary">—</Text>,
    },
    {
      title: '最长可申请',
      dataIndex: 'max_apply_days',
      width: 110,
      align: 'right',
      render: (v: number) => `${v} 天`,
    },
    {
      title: '操作',
      width: 110,
      align: 'right',
      render: (_, r) => (
        <Button type="primary" size="small" onClick={() => openApply(r)}>
          申请授权
        </Button>
      ),
    },
  ]

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
      <Card title="可申请的数据源">
        <Table
          rowKey="id"
          size="middle"
          columns={applyColumns}
          dataSource={applyable}
          loading={loading}
          locale={{
            emptyText: '暂无开放申请的数据源。请联系 DBA 在「数据源管理」中开启申请。',
          }}
          pagination={{
            defaultPageSize: 5,
            showSizeChanger: true,
            pageSizeOptions: [5, 10, 20, 50],
            hideOnSinglePage: true,
            showTotal: (total) => `共 ${total} 个数据源`,
          }}
        />
        <Text type="secondary" style={{ fontSize: 12 }}>
          审批通过后即可查询该数据源下的全部库与表，申请时无需选择库。
        </Text>
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
