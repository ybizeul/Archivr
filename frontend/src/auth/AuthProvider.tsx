import type { ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '../api/client'
import { AuthContext, type AuthState, type User } from './context'

const ME_KEY = ['auth', 'me'] as const

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()

  const me = useQuery({
    queryKey: ME_KEY,
    queryFn: async (): Promise<User | null> => {
      try {
        return await api.get<User>('/auth/me')
      } catch (err) {
        // Not signed in is a normal state, not an error to show.
        if (err instanceof ApiError && err.status === 401) return null
        throw err
      }
    },
  })

  const loginMutation = useMutation({
    mutationFn: (credentials: { email: string; password: string }) =>
      api.post<{ user: User }>('/auth/login', credentials),
    onSuccess: ({ user }) => {
      queryClient.setQueryData(ME_KEY, user)
    },
  })

  const logoutMutation = useMutation({
    mutationFn: () => api.post<{ redirect?: string } | undefined>('/auth/logout'),
    onSettled: () => {
      queryClient.setQueryData(ME_KEY, null)
    },
  })

  const value: AuthState = {
    user: me.data ?? null,
    isLoading: me.isPending,
    login: async (email, password) => {
      await loginMutation.mutateAsync({ email, password })
    },
    logout: async () => {
      const res = await logoutMutation.mutateAsync()
      return res?.redirect ?? null
    },
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
