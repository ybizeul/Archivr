import { useState, type CSSProperties } from 'react'
import { ActionIcon, Box, Group, Tooltip } from '@mantine/core'
import { useElementSize } from '@mantine/hooks'
import { notifications } from '@mantine/notifications'
import { IconRotate, IconRotateClockwise, IconZoomIn, IconZoomOut } from '@tabler/icons-react'
import { usePatchDocument } from '../api/queries'
import { errorMessage } from '../lib/format'

interface DocViewerProps {
  /** The document to show. Not needed when `src` is given. */
  id?: number
  /** Where the file comes from. Defaults to the signed-in file route for `id`. */
  src?: string
  mime: string
  title: string
  /** Degrees clockwise, as stored on the document. */
  rotation?: number
  /** Turn the view only, without saving the rotation. Used where the viewer is not signed in. */
  readOnly?: boolean
}

const normalise = (deg: number) => ((deg % 360) + 360) % 360

/**
 * Shows the stored file: PDFs in the browser's own viewer, images with a click-to-zoom.
 * The rotate buttons turn the view and remember the turn on the document. The file itself
 * is never changed: rotation is only a hint for how the browser draws it.
 */
export function DocViewer({ id, src: srcProp, mime, title, rotation = 0, readOnly = false }: DocViewerProps) {
  const src = srcProp ?? `/api/v1/documents/${id}/file`
  const isPdf = mime === 'application/pdf'
  const [zoomed, setZoomed] = useState(false)
  const [turn, setTurn] = useState(normalise(rotation))
  const patch = usePatchDocument()

  const rotate = (delta: number) => {
    const previous = turn
    const next = normalise(previous + delta)
    setTurn(next)
    if (readOnly || id === undefined) return
    patch.mutate(
      { id, patch: { rotation: next } },
      {
        onError: (err) => {
          setTurn(previous)
          notifications.show({ color: 'red', title: "Couldn't save the rotation", message: errorMessage(err) })
        },
      },
    )
  }

  return (
    <Box className="ax-viewer">
      <Group className="ax-viewer-tools" gap={6} justify="flex-end" wrap="nowrap">
        <Tooltip label="Rotate left">
          <ActionIcon variant="default" size="lg" aria-label="Rotate left" onClick={() => rotate(-90)}>
            <IconRotate size={18} stroke={1.6} />
          </ActionIcon>
        </Tooltip>
        <Tooltip label="Rotate right">
          <ActionIcon variant="default" size="lg" aria-label="Rotate right" onClick={() => rotate(90)}>
            <IconRotateClockwise size={18} stroke={1.6} />
          </ActionIcon>
        </Tooltip>
        {!isPdf && (
          <Tooltip label={zoomed ? 'Fit to sheet' : 'Zoom in'}>
            <ActionIcon
              variant="default"
              size="lg"
              aria-label={zoomed ? 'Fit to sheet' : 'Zoom in'}
              onClick={() => setZoomed((z) => !z)}
            >
              {zoomed ? <IconZoomOut size={18} stroke={1.6} /> : <IconZoomIn size={18} stroke={1.6} />}
            </ActionIcon>
          </Tooltip>
        )}
      </Group>
      {isPdf ? (
        <PdfStage src={src} title={id === undefined ? title : `Document ${id}: ${title}`} turn={turn} />
      ) : (
        <ImageStage src={src} title={title} turn={turn} zoomed={zoomed} onToggleZoom={() => setZoomed((z) => !z)} />
      )}
    </Box>
  )
}

/** A PDF turned with CSS. A quarter turn swaps the frame's width and height, so it still fills the stage. */
function PdfStage({ src, title, turn }: { src: string; title: string; turn: number }) {
  const sideways = turn === 90 || turn === 270
  return (
    <div className="ax-viewer-stage">
      <iframe
        className="ax-viewer-frame"
        src={`${src}#navpanes=0&view=FitH`}
        title={title}
        style={{
          width: sideways ? '100cqh' : '100cqw',
          height: sideways ? '100cqw' : '100cqh',
          transform: `translate(-50%, -50%) rotate(${turn}deg)`,
        }}
      />
    </div>
  )
}

/**
 * An image turned with CSS. The turned image keeps its layout box, so the box is sized by hand:
 * a quarter turn swaps its width and height, and the scroll area follows the turned shape.
 */
function ImageStage({
  src,
  title,
  turn,
  zoomed,
  onToggleZoom,
}: {
  src: string
  title: string
  turn: number
  zoomed: boolean
  onToggleZoom: () => void
}) {
  const { ref, width: available } = useElementSize<HTMLDivElement>()
  const [natural, setNatural] = useState<{ w: number; h: number } | null>(null)
  const sideways = turn === 90 || turn === 270

  let box: CSSProperties | undefined
  let img: CSSProperties | undefined
  if (natural && available > 0) {
    // The width the turned image shows at: the sheet width, never past its natural size, or 180% when zoomed.
    const naturalShown = sideways ? natural.h : natural.w
    const shown = zoomed ? available * 1.8 : Math.min(available, naturalShown)
    const scale = shown / naturalShown
    const w = natural.w * scale
    const h = natural.h * scale
    box = { position: 'relative', width: sideways ? h : w, height: sideways ? w : h, margin: '0 auto' }
    img = {
      position: 'absolute',
      top: '50%',
      left: '50%',
      width: w,
      height: h,
      maxWidth: 'none',
      transform: `translate(-50%, -50%) rotate(${turn}deg)`,
    }
  }

  return (
    <Box className="ax-viewer-image">
      <div ref={ref}>
        <div style={box}>
          <img
            src={src}
            alt={title}
            className={zoomed ? 'ax-image zoomed' : 'ax-image'}
            style={img}
            onLoad={(e) => setNatural({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
            onClick={onToggleZoom}
          />
        </div>
      </div>
    </Box>
  )
}
