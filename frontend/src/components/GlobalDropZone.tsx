import { useEffect, useRef, useState } from 'react'
import { Box, Stack, Text } from '@mantine/core'
import { IconUpload } from '@tabler/icons-react'
import { useFileUpload } from '../lib/useFileUpload'

const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes('Files')

/** Lets the user drop files anywhere in the window to add them to the inbox. */
export function GlobalDropZone() {
  const { uploadFiles } = useFileUpload()
  const [active, setActive] = useState(false)
  // dragenter/dragleave fire for every child element, so count them to know when the pointer really left.
  const depth = useRef(0)
  const send = useRef(uploadFiles)
  useEffect(() => {
    send.current = uploadFiles
  })

  useEffect(() => {
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth.current++
      setActive(true)
    }
    const over = (e: DragEvent) => {
      if (!hasFiles(e)) return
      // Required for the drop event to fire, and stops the browser from opening the file.
      e.preventDefault()
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
    }
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth.current = Math.max(0, depth.current - 1)
      if (depth.current === 0) setActive(false)
    }
    const drop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      depth.current = 0
      setActive(false)
      const files = Array.from(e.dataTransfer?.files ?? [])
      if (files.length > 0) void send.current(files)
    }
    window.addEventListener('dragenter', enter)
    window.addEventListener('dragover', over)
    window.addEventListener('dragleave', leave)
    window.addEventListener('drop', drop)
    return () => {
      window.removeEventListener('dragenter', enter)
      window.removeEventListener('dragover', over)
      window.removeEventListener('dragleave', leave)
      window.removeEventListener('drop', drop)
    }
  }, [])

  if (!active) return null
  return (
    <Box
      aria-hidden
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 1000,
        pointerEvents: 'none',
        background: 'color-mix(in srgb, var(--mantine-color-prussian-6) 14%, transparent)',
        border: '3px dashed var(--mantine-color-prussian-6)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <Stack align="center" gap="xs" p="xl" bg="var(--mantine-color-body)" style={{ borderRadius: 'var(--mantine-radius-md)' }}>
        <IconUpload size={32} stroke={1.6} />
        <Text fw={600}>Drop to add to the inbox</Text>
      </Stack>
    </Box>
  )
}
