import type { ReactNode } from 'react'
import { Center, Loader } from '@mantine/core'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { useAuth } from './auth/context'
import { AppLayout } from './components/AppLayout'
import { AdminPage } from './pages/AdminPage'
import { DocumentPage } from './pages/DocumentPage'
import { LibraryPage } from './pages/LibraryPage'
import { LoginPage } from './pages/LoginPage'
import { TriagePage } from './pages/TriagePage'

function RequireAuth({ children }: { children: ReactNode }) {
  const { user, isLoading } = useAuth()
  const location = useLocation()

  if (isLoading) {
    return (
      <Center mih="100vh">
        <Loader size="sm" />
      </Center>
    )
  }
  if (!user) {
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  }
  return children
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <RequireAuth>
            <AppLayout />
          </RequireAuth>
        }
      >
        <Route index element={<TriagePage />} />
        <Route path="library" element={<LibraryPage />} />
        <Route path="documents/:id" element={<DocumentPage />} />
        <Route path="admin" element={<AdminPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
