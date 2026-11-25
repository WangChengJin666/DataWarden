import { useState } from 'react'
import { Alert, Button, Input, Modal, Space, Typography } from 'antd'
import { CopyOutlined } from '@ant-design/icons'

const { Text } = Typography

interface Props {
  /** 明文密码；非 null 即弹窗打开 */
  password: string | null
  /** 这个密码是给谁的，用在文案里 */
  username: string
  /** 是新建还是重置，只影响标题措辞 */
  mode: 'created' | 'reset'
  onClose: () => void
}

/**
 * 一次性密码展示。
 *
 * 单独抽成组件而不是在用户页里内联两遍（新建一处、重置一处）：
 * 「明文只出现一次」这件事的约束体现在文案、复制按钮与关闭行为上，
 * 写两遍很容易在两处措辞不一致，而用户只会记住其中一处。
 *
 * 不做自动复制到剪贴板：粘贴板是全局的，自动写入等于让密码留在
 * 一个所有程序都能读的地方，而且用户往往没意识到。
 */
export default function PasswordOnceModal({ password, username, mode, onClose }: Props) {
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    if (!password) return
    try {
      await navigator.clipboard.writeText(password)
      setCopied(true)
    } catch {
      // 浏览器可能因非 HTTPS 或权限拒绝而失败。此时不报错：
      // 输入框是可选中复制的，用户可以手动复制。
    }
  }

  return (
    <Modal
      title={mode === 'created' ? '用户已创建' : '密码已重置'}
      open={password !== null}
      onCancel={onClose}
      onOk={onClose}
      okText="我已记录"
      cancelButtonProps={{ style: { display: 'none' } }}
      maskClosable={false}
      width={520}
    >
      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 16 }}
        message="初始密码只显示这一次"
        description="关闭后无法再次查看。请立即通过安全渠道转交给本人，并提醒他妥善保存。"
      />
      <Space direction="vertical" style={{ width: '100%' }} size={8}>
        <div>
          <Text type="secondary">{mode === 'created' ? '新账号' : '账号'}：</Text>
          <Text strong>{username}</Text>
        </div>
        <Input
          value={password ?? ''}
          readOnly
          size="large"
          style={{ fontFamily: 'monospace', letterSpacing: 1 }}
          addonAfter={
            <Button type="text" size="small" icon={<CopyOutlined />} onClick={copy}>
              {copied ? '已复制' : '复制'}
            </Button>
          }
        />
      </Space>
    </Modal>
  )
}
