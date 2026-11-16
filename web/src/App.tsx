import { Navigate, Route, Routes } from 'react-router-dom'
import AppLayout from './AppLayout'
import LoginPage from './pages/LoginPage'
import QueryPage from './pages/QueryPage'
import ApplyPage from './pages/ApplyPage'
import DatasourcePage from './pages/DatasourcePage'
import ReviewPage from './pages/ReviewPage'
import { getToken } from './api/client'

function RequireAuth({ children }: { children: React.ReactNode }) {
  if (!getToken()) {
    return <Navigate to="/login" replace />
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
        <Route path="datasources" element={<DatasourcePage />} />
        <Route path="review" element={<ReviewPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
