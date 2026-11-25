import { useMemo } from 'react'
import { Layout, Menu, Dropdown, Avatar, Space, Tag, Typography } from 'antd'
import type { MenuProps } from 'antd'
import {
  DatabaseOutlined,
  FileProtectOutlined,
  LogoutOutlined,
  SearchOutlined,
  UserOutlined,
  AuditOutlined,
  SafetyCertificateOutlined,
  TeamOutlined,
  UsergroupAddOutlined,
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
  // 账号管理单独一档权限：dba 不能管账号，否则能把自己提成 superadmin
  const isSuperAdmin = user?.role === 'superadmin'

  const items = useMemo(() => {
    // 数据源管理只对 DBA / 超级管理员开放：
    // 里面是主机、端口、库名、连接账号和密码配置，开发者用不到，
    // 也不该看到。不放在菜单里，避免点进去才发现无权操作。
    //
    // 显式声明为 MenuProps['items']：让「带 icon 的菜单项」与
    // 「带字符串 label 的分组」能放进同一个数组。让 TS 自行推断时，
    // 数组元素类型会被首个元素锁定成 { icon: Element; label: Element }，
    // 后面 push 分组项就会报「string 不能赋给 Element」。
    const base: MenuProps['items'] = [
      { key: '/query', icon: <SearchOutlined />, label: <Link to="/query">数据查询</Link> },
      { key: '/apply', icon: <FileProtectOutlined />, label: <Link to="/apply">申请授权</Link> },
    ]
    if (isDBA) {
      base.push(
        {
          key: '/datasources',
          icon: <DatabaseOutlined />,
          label: <Link to="/datasources">数据源管理</Link>,
        },
        {
          key: '/review',
          icon: <AuditOutlined />,
          label: <Link to="/review">授权审批</Link>,
        },
        {
          key: '/grants',
          icon: <SafetyCertificateOutlined />,
          label: <Link to="/grants">授权管理</Link>,
        },
      )
    }
    // 账号管理只给超级管理员：组与用户是整个平台的权限底座，
    // dba 拿不到，避免「dba 给自己提权」这条路径。
    //
    // 刻意不用 type:'group' 做分组：antd 的分组项渲染成不可点击的标题
    // （icon 也会被忽略），点它不会展开子项，用户必须再去点子项——
    // 只有两项时，多一层分组反而多一次点击，还丢了图标这个视觉锚点。
    // 等账号管理长出第三项（比如审计查询、系统配置）再考虑分组。
    if (isSuperAdmin) {
      base.push(
        {
          key: '/users',
          icon: <UsergroupAddOutlined />,
          label: <Link to="/users">用户管理</Link>,
        },
        { key: '/groups', icon: <TeamOutlined />, label: <Link to="/groups">组管理</Link> },
      )
    }
    return base
  }, [isDBA, isSuperAdmin])

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
