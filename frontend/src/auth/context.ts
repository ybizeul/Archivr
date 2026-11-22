import { createContext, useContext } from 'react'

export type Role = 'admin' | 'user'

export interface User {
  id: number
  email: string
  name: string
  role: Role
}

export interface AuthState {
  user: User | null
  isLoading: boolean
  login: (email: string, password: string) => Promise<void>
  // Resolves to the provider's logout URL when SSO should end the provider session too, else null.
  logout: () => Promise<string | null>
}

export const AuthContext = createContext<AuthState | undefined>(undefined)

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}
