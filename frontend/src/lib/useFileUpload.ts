import { useCallback } from 'react'
import { notifications } from '@mantine/notifications'
import { useNavigate } from 'react-router-dom'
import { useUploadDocument } from '../api/queries'
import { errorMessage } from './format'

/**
 * Sends files to the INBOX one after the other. Duplicates and unsupported files are reported, not silently dropped.
 * The valid-until date is left to the inbox panel, so nothing is asked here.
 */
export function useFileUpload() {
  const upload = useUploadDocument()
  const navigate = useNavigate()

  const uploadFiles = useCallback(
    async (files: File[]) => {
      let last: { id: number } | null = null
      let added = 0
      for (const file of files) {
        try {
          const doc = await upload.mutateAsync({ file })
          last = doc
          added++
          if (files.length === 1) {
            notifications.show({ color: 'prussian', title: 'Added to the inbox', message: `No. ${doc.id} · ${doc.title}` })
          }
        } catch (err) {
          const code = (err as Error & { code?: string }).code
          notifications.show({
            color: 'red',
            title: code === 'DUPLICATE' ? 'Already in the archive' : "Couldn't add this file",
            message: files.length > 1 ? `${file.name}: ${errorMessage(err)}` : errorMessage(err),
          })
        }
      }
      if (added > 1) {
        notifications.show({ color: 'prussian', title: 'Added to the inbox', message: `${added} files` })
        void navigate('/inbox')
      } else if (added === 1 && last) {
        void navigate(`/inbox?doc=${last.id}`)
      }
    },
    [upload, navigate],
  )

  return { uploadFiles, isPending: upload.isPending }
}
