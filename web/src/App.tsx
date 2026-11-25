import { Navigate, Route, Routes } from 'react-router-dom'
import AppLayout from './AppLayout'
import LoginPage from './pages/LoginPage'
import QueryPage from './pages/QueryPage'
import ApplyPage from './pages/ApplyPage'
import DatasourcePage from './pages/DatasourcePage'
import ReviewPage from './pages/ReviewPage'
import GrantsPage from './pages/GrantsPage'
import GroupsPage from './pages/GroupsPage'
import UsersPage from './pages/UsersPage'
import { getCurrentUser, getToken } from './api/client'

function RequireAuth({ children }: { children: React.ReactNode }) {
  if (!getToken()) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}

// RequireDBA 拦住开发者直接输入 /datasources 这类地址。
// 菜单里已经隐藏了入口，这里是第二道：后端接口同样只对 DBA 开放，
// 前端拦不拦都不影响安全，但能让用户看到一句明确的话而不是一片报错。
function RequireDBA({ children }: { children: React.ReactNode }) {
  const role = getCurrentUser()?.role ?? ''
  if (!['dba', 'superadmin'].includes(role)) {
    return <Navigate to="/apply" replace />
  }
  return <>{children}</>
}

// RequireSuperAdmin 拦住直接输入 /users、/groups 地址的非超管。
// 菜单里已经隐藏了入口，这里是第二道：后端接口同样只对超管开放，
// 前端拦不拦都不影响安全，但能让用户看到明确的重定向而不是一片报错。
function RequireSuperAdmin({ children }: { children: React.ReactNode }) {
  const role = getCurrentUser()?.role ?? ''
  if (role !== 'superadmin') {
    return <Navigate to="/query" replace />
  }
  return <>{children}</>
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        path="/"
        element={
          <RequireAuth>
            <AppLayout />
          </RequireAuth>
        }
      >
        <Route index element={<Navigate to="/query" replace />} />
        <Route path="query" element={<QueryPage />} />
        <Route path="apply" element={<ApplyPage />} />
        <Route
          path="datasources"
          element={
            <RequireDBA>
              <DatasourcePage />
            </RequireDBA>
          }
        />
        <Route path="review" element={<ReviewPage />} />
        <Route
          path="grants"
          element={
            <RequireDBA>
              <GrantsPage />
            </RequireDBA>
          }
        />
        <Route
          path="groups"
          element={
            <RequireSuperAdmin>
              <GroupsPage />
            </RequireSuperAdmin>
          }
        />
        <Route
          path="users"
          element={
            <RequireSuperAdmin>
              <UsersPage />
            </RequireSuperAdmin>
          }
        />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
