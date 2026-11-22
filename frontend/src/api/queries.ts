// TanStack Query hooks for the Archivr API. Query keys are kept here so that
// every mutation invalidates exactly the views it changes.
import { useMemo } from 'react'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './client'
import type {
  AuthSettings,
  Correspondent,
  DocType,
  Document,
  DocumentDetail,
  DocumentPatch,
  Folder,
  IMAPSettings,
  InboxCount,
  OCRSettings,
  Page,
  PaperlessMapping,
  PaperlessProgress,
  PaperlessSettings,
  ShareLink,
  SharedDocument,
  Suggestions,
  Tag,
  UserRow,
} from './types'

export interface DocFilters {
  q?: string
  folder?: number
  subtree?: boolean
  type?: number
  tag?: string
  from?: string
  to?: string
  ocr?: string
  sort?: string
  inbox?: boolean
  limit?: number
  offset?: number
}

function toQuery(params: Record<string, string | number | boolean | undefined>): string {
  const sp = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '' || v === false) continue
    sp.set(k, v === true ? '1' : String(v))
  }
  const s = sp.toString()
  return s ? `?${s}` : ''
}

export const keys = {
  inboxCount: ['inbox', 'count'] as const,
  documents: (f: DocFilters) => ['documents', f] as const,
  document: (id: number) => ['document', id] as const,
  suggestions: (id: number) => ['suggestions', id] as const,
  folders: ['folders'] as const,
  types: ['types'] as const,
  correspondents: (q = '') => ['correspondents', q] as const,
  tags: (q = '') => ['tags', q] as const,
  adminAuth: ['admin', 'auth'] as const,
  adminIMAP: ['admin', 'imap'] as const,
  adminOCR: ['admin', 'ocr'] as const,
  adminPaperless: ['admin', 'paperless'] as const,
  adminPaperlessImport: ['admin', 'paperless', 'import'] as const,
  adminPaperlessMapping: ['admin', 'paperless', 'mapping'] as const,
  adminUsers: ['admin', 'users'] as const,
}

export function useInboxCount() {
  return useQuery({
    queryKey: keys.inboxCount,
    queryFn: () => api.get<InboxCount>('/inbox/count'),
    refetchInterval: 30_000,
  })
}

export function useDocuments(f: DocFilters, options?: { enabled?: boolean }) {
  const qs = toQuery({
    q: f.q,
    folder: f.folder,
    subtree: f.subtree,
    type: f.type,
    tag: f.tag,
    from: f.from,
    to: f.to,
    ocr: f.ocr,
    sort: f.sort,
    inbox: f.inbox,
    limit: f.limit ?? 50,
    offset: f.offset ?? 0,
  })
  return useQuery({
    queryKey: keys.documents(f),
    queryFn: () => api.get<Page<Document>>(`/documents${qs}`),
    placeholderData: (prev) => prev,
    enabled: options?.enabled ?? true,
  })
}

// The server returns at most 200 documents per request.
const INBOX_PAGE_SIZE = 200

/**
 * The whole inbox, most recently imported first. Pages are loaded one after another: the caller keeps
 * calling fetchNextPage while hasNextPage is true, so every document ends up in items.
 */
export function useInboxQueue() {
  const query = useInfiniteQuery({
    queryKey: ['documents', 'inbox-queue'] as const,
    queryFn: ({ pageParam }) =>
      api.get<Page<Document>>(
        `/documents${toQuery({ inbox: true, sort: 'newest', limit: INBOX_PAGE_SIZE, offset: pageParam })}`,
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => {
      const loaded = last.offset + last.items.length
      return last.items.length > 0 && loaded < last.total ? loaded : undefined
    },
  })

  // Pages can shift while documents are being filed, so the same document may appear twice.
  const items = useMemo(() => {
    const seen = new Set<number>()
    const out: Document[] = []
    for (const page of query.data?.pages ?? []) {
      for (const d of page.items) {
        if (!seen.has(d.id)) {
          seen.add(d.id)
          out.push(d)
        }
      }
    }
    return out
  }, [query.data])

  return {
    items,
    total: query.data?.pages.at(-1)?.total ?? 0,
    isPending: query.isPending,
    hasNextPage: query.hasNextPage,
    isFetchingNextPage: query.isFetchingNextPage,
    isFetchNextPageError: query.isFetchNextPageError,
    fetchNextPage: query.fetchNextPage,
  }
}

export function useDocument(id: number | undefined) {
  return useQuery({
    queryKey: id ? keys.document(id) : ['document', 'none'],
    queryFn: () => api.get<DocumentDetail>(`/documents/${id}`),
    enabled: id !== undefined,
    // OCR runs in the background. Keep polling while a document is still pending.
    refetchInterval: (q) => (q.state.data?.ocr_status === 'pending' ? 4000 : false),
  })
}

export function useSuggestions(id: number | undefined) {
  return useQuery({
    queryKey: id ? keys.suggestions(id) : ['suggestions', 'none'],
    queryFn: () => api.get<Suggestions>(`/documents/${id}/suggestions`),
    enabled: id !== undefined,
  })
}

export function useFolders() {
  return useQuery({
    queryKey: keys.folders,
    queryFn: () => api.get<{ items: Folder[] }>('/folders').then((r) => r.items),
  })
}

export function useTypes() {
  return useQuery({
    queryKey: keys.types,
    queryFn: () => api.get<{ items: DocType[] }>('/types').then((r) => r.items),
  })
}

/** The correspondent value list. A name typed on a document that is not here yet is added when it is saved. */
export function useCorrespondents(q = '') {
  return useQuery({
    queryKey: keys.correspondents(q),
    queryFn: () => api.get<{ items: Correspondent[] }>(`/correspondents${toQuery({ q })}`).then((r) => r.items),
  })
}

export function useTags(q = '') {
  return useQuery({
    queryKey: keys.tags(q),
    queryFn: () => api.get<{ items: Tag[] }>(`/tags${toQuery({ q })}`).then((r) => r.items),
  })
}

/** Invalidate everything that a document change can affect. */
export function useInvalidateDocuments() {
  const qc = useQueryClient()
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: ['documents'] }),
      qc.invalidateQueries({ queryKey: ['document'] }),
      qc.invalidateQueries({ queryKey: keys.inboxCount }),
      qc.invalidateQueries({ queryKey: keys.folders }),
      qc.invalidateQueries({ queryKey: ['correspondents'] }),
      qc.invalidateQueries({ queryKey: keys.types }),
      qc.invalidateQueries({ queryKey: ['tags'] }),
      qc.invalidateQueries({ queryKey: ['suggestions'] }),
    ])
}

export function usePatchDocument() {
  const invalidate = useInvalidateDocuments()
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: DocumentPatch }) =>
      api.patch<Document>(`/documents/${id}`, patch),
    onSuccess: () => invalidate(),
  })
}

export function useDeleteDocument() {
  const invalidate = useInvalidateDocuments()
  return useMutation({
    mutationFn: (id: number) => api.del<void>(`/documents/${id}`),
    onSuccess: () => invalidate(),
  })
}

/** Signs a link to one document, valid for `expiresInSeconds`. Nothing changes on the document. */
export function useCreateShare() {
  return useMutation({
    mutationFn: ({ id, expiresInSeconds }: { id: number; expiresInSeconds: number }) =>
      api.post<ShareLink>(`/documents/${id}/share`, { expires_in_seconds: expiresInSeconds }),
  })
}

/** The document a share link opens. Needs no sign-in. */
export function useSharedDocument(token: string | undefined) {
  return useQuery({
    queryKey: ['share', token],
    queryFn: () => api.get<SharedDocument>(`/shares/${encodeURIComponent(token ?? '')}`),
    enabled: !!token,
    // A bad or expired link will not get better by asking again.
    retry: false,
    staleTime: Infinity,
  })
}

export function useUploadDocument() {
  const invalidate = useInvalidateDocuments()
  return useMutation({
    mutationFn: async (file: File) => {
      const body = new FormData()
      body.append('file', file)
      const res = await fetch('/api/v1/documents', { method: 'POST', body, credentials: 'same-origin' })
      if (!res.ok) {
        const payload = (await res.json().catch(() => ({}))) as { error?: { code?: string; message?: string } }
        const err = new Error(payload.error?.message ?? 'Upload failed') as Error & { code?: string; status?: number }
        err.code = payload.error?.code
        err.status = res.status
        throw err
      }
      return (await res.json()) as Document
    },
    onSuccess: () => invalidate(),
  })
}

export function useReOCR() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post<Document>(`/documents/${id}/reocr`),
    onSuccess: (_doc, id) => {
      void qc.invalidateQueries({ queryKey: keys.document(id) })
    },
  })
}

export function useFolderMutations() {
  const invalidate = useInvalidateDocuments()
  const create = useMutation({
    mutationFn: (body: { name: string; parent_id?: number | null }) => api.post<Folder>('/folders', body),
    onSuccess: () => invalidate(),
  })
  const update = useMutation({
    mutationFn: ({ id, ...body }: { id: number; name?: string; parent_id?: number }) =>
      api.patch<Folder>(`/folders/${id}`, body),
    onSuccess: () => invalidate(),
  })
  const remove = useMutation({
    mutationFn: (id: number) => api.del<void>(`/folders/${id}`),
    onSuccess: () => invalidate(),
  })
  return { create, update, remove }
}

export function useTypeMutations() {
  const qc = useQueryClient()
  const refresh = () => qc.invalidateQueries({ queryKey: keys.types })
  const create = useMutation({
    mutationFn: (body: { name: string; color: string }) => api.post<DocType>('/types', body),
    onSuccess: refresh,
  })
  const update = useMutation({
    mutationFn: ({ id, ...body }: { id: number; name: string; color: string }) =>
      api.patch<DocType>(`/types/${id}`, body),
    onSuccess: () => {
      void refresh()
      void qc.invalidateQueries({ queryKey: ['documents'] })
    },
  })
  const remove = useMutation({
    mutationFn: (id: number) => api.del<void>(`/types/${id}`),
    onSuccess: refresh,
  })
  return { create, update, remove }
}

export function useTagMutations() {
  const invalidate = useInvalidateDocuments()
  const rename = useMutation({
    mutationFn: ({ id, name }: { id: number; name: string }) => api.patch<Tag>(`/tags/${id}`, { name }),
    onSuccess: () => invalidate(),
  })
  const remove = useMutation({
    mutationFn: (id: number) => api.del<void>(`/tags/${id}`),
    onSuccess: () => invalidate(),
  })
  return { rename, remove }
}

// Admin

export function useAuthSettings() {
  return useQuery({ queryKey: keys.adminAuth, queryFn: () => api.get<AuthSettings>('/admin/settings/auth') })
}

export function useIMAPSettings() {
  return useQuery({ queryKey: keys.adminIMAP, queryFn: () => api.get<IMAPSettings>('/admin/settings/imap') })
}

export function usePaperlessSettings() {
  return useQuery({
    queryKey: keys.adminPaperless,
    queryFn: () => api.get<PaperlessSettings>('/admin/settings/paperless'),
  })
}

export function usePaperlessMapping() {
  return useQuery({
    queryKey: keys.adminPaperlessMapping,
    queryFn: () => api.get<PaperlessMapping>('/admin/paperless/mapping'),
  })
}

/** Progress of the Paperless import. Polls while a run is in progress. */
export function usePaperlessImport() {
  return useQuery({
    queryKey: keys.adminPaperlessImport,
    queryFn: () => api.get<PaperlessProgress>('/admin/paperless/import'),
    refetchInterval: (q) => (q.state.data?.state === 'running' ? 1000 : false),
  })
}

export function useOCRSettings() {
  return useQuery({ queryKey: keys.adminOCR, queryFn: () => api.get<OCRSettings>('/admin/settings/ocr') })
}

export function useUsers() {
  return useQuery({
    queryKey: keys.adminUsers,
    queryFn: () => api.get<{ items: UserRow[] }>('/admin/users').then((r) => r.items),
  })
}

export function useUserMutations() {
  const qc = useQueryClient()
  const refresh = () => qc.invalidateQueries({ queryKey: keys.adminUsers })
  const create = useMutation({
    mutationFn: (body: { email: string; name: string; role: string; password?: string }) =>
      api.post<UserRow>('/admin/users', body),
    onSuccess: refresh,
  })
  const update = useMutation({
    mutationFn: ({ id, ...body }: { id: number; name?: string; role?: string }) =>
      api.patch<UserRow>(`/admin/users/${id}`, body),
    onSuccess: refresh,
  })
  const remove = useMutation({
    mutationFn: (id: number) => api.del<void>(`/admin/users/${id}`),
    onSuccess: refresh,
  })
  const setPassword = useMutation({
    mutationFn: ({ id, password }: { id: number; password: string }) =>
      api.post<void>(`/admin/users/${id}/password`, { password }),
    onSuccess: refresh,
  })
  return { create, update, remove, setPassword }
}
