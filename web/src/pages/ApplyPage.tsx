import { useEffect, useMemo, useState } from 'react'
import {
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  InputNumber,
  message,
  Modal,
  Select,
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
    form.setFieldsValue({ days: Math.min(7, ds.max_apply_days) })
    setModalOpen(true)
  }

  // 根据选中数据源的可申请范围，生成库→表选项
  const scopeOptions = useMemo(() => {
    if (!selected) return []
    return selected.apply_scopes.map((s) => ({
      label: s.database,
      options: (s.tables.length ? s.tables : ['*']).map((t) => ({
        label: t === '*' ? `${s.database}（全部表）` : `${s.database}:${t}`,
        value: t === '*' ? s.database : `${s.database}:${t}`,
      })),
    }))
  }, [selected])

  const submit = async () => {
    if (!selected) return
    try {
      const values = await form.validateFields()
      await createGrantRequest({
        datasource_id: selected.id,
        scope: values.scope,
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
    { title: '数据源', dataIndex: 'datasource_name', width: 140 },
    {
      title: '申请范围',
      dataIndex: 'scope',
      width: 200,
      render: (v: string) => <Text code>{v}</Text>,
    },
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
            暂无开放申请的数据源。请联系 DBA 在「数据源管理」中开启申请并配置可申请范围。
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
                      <Descriptions.Item label="可申请范围">
                        <Space wrap size={4}>
                          {ds.apply_scopes.map((s, i) => (
                            <Tag key={i}>
                              {s.database}
                              {s.tables.length ? `: ${s.tables.join(',')}` : ' 全部表'}
                            </Tag>
                          ))}
                        </Space>
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
          <Form.Item
            name="scope"
            label="申请范围"
            rules={[{ required: true, message: '请选择范围' }]}
            extra="只能选择管理员开放的范围"
          >
            <Select
              placeholder="选择库或表"
              options={scopeOptions}
              showSearch
              optionFilterProp="label"
            />
          </Form.Item>
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
