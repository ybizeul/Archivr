import { useState } from 'react'
import { ActionIcon, Box, Tooltip } from '@mantine/core'
import { IconZoomIn, IconZoomOut } from '@tabler/icons-react'

interface DocViewerProps {
  id: number
  mime: string
  title: string
}

/** Shows the stored file: PDFs in the browser's own viewer, images with a click-to-zoom. */
export function DocViewer({ id, mime, title }: DocViewerProps) {
  const src = `/api/v1/documents/${id}/file`
  const [zoomed, setZoomed] = useState(false)

  if (mime === 'application/pdf') {
    return (
      <iframe
        key={id}
        className="ax-viewer-frame"
        src={`${src}#navpanes=0&view=FitH`}
        title={`Document ${id}: ${title}`}
      />
    )
  }

  return (
    <Box className="ax-viewer-image" style={{ position: 'relative' }}>
      <Tooltip label={zoomed ? 'Fit to sheet' : 'Zoom in'}>
        <ActionIcon
          variant="default"
          size="lg"
          aria-label={zoomed ? 'Fit to sheet' : 'Zoom in'}
          onClick={() => setZoomed((z) => !z)}
          style={{ position: 'sticky', top: 8, float: 'right', margin: 8, zIndex: 1 }}
        >
          {zoomed ? <IconZoomOut size={18} stroke={1.6} /> : <IconZoomIn size={18} stroke={1.6} />}
        </ActionIcon>
      </Tooltip>
      <img
        key={id}
        src={src}
        alt={title}
        className={zoomed ? 'ax-image zoomed' : 'ax-image'}
        onClick={() => setZoomed((z) => !z)}
      />
    </Box>
  )
}
