import { useMemo } from 'react'
import { Layout, Menu, Dropdown, Avatar, Space, Tag, Typography } from 'antd'
import {
  DatabaseOutlined,
  FileProtectOutlined,
  LogoutOutlined,
  SearchOutlined,
  UserOutlined,
  AuditOutlined,
} from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { clearSession, getCurrentUser } from '@/api/client'

const { Header, Sider, Content } = Layout
const { Text } = Typography

const roleLabels: Record<string, { label: string; color: string }> = {
  developer: { label: '开发者', color: 'default' },
  dba: { label: 'DBA', color: 'blue' },
  superadmin: { label: '超级管理员', color: 'red' },
}

export default function AppLayout() {
  const nav = useNavigate()
  const loc = useLocation()
  const user = getCurrentUser()
  const isDBA = ['dba', 'superadmin'].includes(user?.role ?? '')

  const items = useMemo(() => {
    const base = [
      { key: '/query', icon: <SearchOutlined />, label: <Link to="/query">数据查询</Link> },
      { key: '/apply', icon: <FileProtectOutlined />, label: <Link to="/apply">申请授权</Link> },
      {
        key: '/datasources',
        icon: <DatabaseOutlined />,
        label: <Link to="/datasources">数据源管理</Link>,
      },
    ]
    if (isDBA) {
      base.push({
        key: '/review',
        icon: <AuditOutlined />,
        label: <Link to="/review">授权审批</Link>,
      })
    }
    return base
  }, [isDBA])

  const logout = () => {
    clearSession()
    nav('/login', { replace: true })
  }

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider theme="light" width={208} style={{ borderRight: '1px solid #f0f0f0' }}>
        <div
          style={{
            height: 56,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontWeight: 600,
            fontSize: 16,
            borderBottom: '1px solid #f0f0f0',
          }}
        >
          DataWarden
        </div>
        <Menu mode="inline" selectedKeys={[loc.pathname]} items={items} style={{ border: 0 }} />
      </Sider>
      <Layout>
        <Header
          style={{
            background: '#fff',
            padding: '0 24px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'flex-end',
            borderBottom: '1px solid #f0f0f0',
            height: 56,
          }}
        >
          <Dropdown
            menu={{
              items: [{ key: 'logout', icon: <LogoutOutlined />, label: '退出登录', onClick: logout }],
            }}
          >
            <Space style={{ cursor: 'pointer' }}>
              <Avatar size="small" icon={<UserOutlined />} />
              <Text>{user?.username ?? '未登录'}</Text>
              <Tag color={roleLabels[user?.role ?? '']?.color ?? 'default'}>
                {roleLabels[user?.role ?? '']?.label ?? user?.role}
              </Tag>
            </Space>
          </Dropdown>
        </Header>
        <Content style={{ padding: 24, background: '#f5f5f5' }}>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  )
}
