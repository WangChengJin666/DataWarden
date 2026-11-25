import { useEffect, useState } from 'react'
import { Alert, Form, Input, Modal, message } from 'antd'
import { LockOutlined } from '@ant-design/icons'
import { changeOwnPassword } from '@/api/client'

/** 与后端 minPasswordLen / maxPasswordLen 保持一致。 */
const MIN_LEN = 8
const MAX_LEN = 72

interface Props {
  open: boolean
  onClose: () => void
}

interface FormValues {
  old_password: string
  new_password: string
  confirm_password: string
}

/**
 * 修改自己的密码。
 *
 * 放在顶栏下拉而不是单独一页：这个操作一年用不了几次，
 * 占一个菜单项不划算；而且「系统管理」分组只有超管看得到，
 * 开发者根本没有地方进入。
 *
 * 校验规则刻意与后端一致（8–72、首尾无空白、不能与旧密码相同）：
 * 前端只是把错误提前显示，真正的判定仍在后端——绕开界面直接调接口
 * 也必须被拦住。
 */
export default function PasswordChangeModal({ open, onClose }: Props) {
  const [form] = Form.useForm<FormValues>()
  const [submitting, setSubmitting] = useState(false)

  // 每次打开重置表单：上一次失败留下的红色错误提示残留，
  // 会让用户以为这次一进来就已经出错了。
  useEffect(() => {
    if (open) form.resetFields()
  }, [open, form])

  const submit = async () => {
    let values: FormValues
    try {
      values = await form.validateFields()
    } catch {
      return // 校验失败，antd 已在字段下展示原因
    }

    setSubmitting(true)
    try {
      await changeOwnPassword(values.old_password, values.new_password)
      message.success('密码已更新')
      onClose()
    } catch (e) {
      // 把后端文案挂在「当前密码」字段下：绝大多数失败原因都是旧密码打错，
      // 弹一个全局 toast 然后清空表单，用户还得猜是哪个框有问题。
      form.setFields([
        { name: 'old_password', errors: [e instanceof Error ? e.message : '修改失败'] },
      ])
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal
      title="修改密码"
      open={open}
      onOk={submit}
      okText="确认修改"
      cancelText="取消"
      confirmLoading={submitting}
      onCancel={() => {
        if (!submitting) onClose()
      }}
      // 关闭时不销毁表单，否则 useEffect 里 resetFields 拿到的实例已经卸载；
      // 重置交给上面的 effect 做。
      destroyOnClose={false}
      maskClosable={!submitting}
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message="修改后当前登录保持有效"
        description="本次登录不会退出，但其他设备上已登录的会话最多还会持续到原会话过期，建议你在公用电脑上直接退出登录。"
      />
      <Form form={form} layout="vertical" requiredMark={false} preserve={false}>
        <Form.Item
          name="old_password"
          label="当前密码"
          rules={[{ required: true, message: '请输入当前密码' }]}
        >
          <Input.Password prefix={<LockOutlined />} autoComplete="current-password" />
        </Form.Item>
        <Form.Item
          name="new_password"
          label="新密码"
          rules={[
            { required: true, message: '请输入新密码' },
            {
              validator: (_, value: string) => {
                if (!value) return Promise.resolve()
                if (value.trim() !== value) {
                  return Promise.reject(new Error('首尾不能有空白字符'))
                }
                if (value.length < MIN_LEN) {
                  return Promise.reject(new Error(`至少 ${MIN_LEN} 个字符`))
                }
                if (value.length > MAX_LEN) {
                  return Promise.reject(new Error(`不能超过 ${MAX_LEN} 个字符`))
                }
                if (value === form.getFieldValue('old_password')) {
                  return Promise.reject(new Error('不能与当前密码相同'))
                }
                return Promise.resolve()
              },
            },
          ]}
          // 新密码改了要重跑「是否与旧密码相同」的校验。
          dependencies={['old_password']}
        >
          <Input.Password prefix={<LockOutlined />} autoComplete="new-password" />
        </Form.Item>
        <Form.Item
          name="confirm_password"
          label="确认新密码"
          dependencies={['new_password']}
          rules={[
            { required: true, message: '请再输入一次新密码' },
            ({ getFieldValue }) => ({
              validator: (_, value: string) => {
                if (!value || value === getFieldValue('new_password')) {
                  return Promise.resolve()
                }
                return Promise.reject(new Error('两次输入的新密码不一致'))
              },
            }),
          ]}
        >
          <Input.Password prefix={<LockOutlined />} autoComplete="new-password" />
        </Form.Item>
      </Form>
    </Modal>
  )
}
