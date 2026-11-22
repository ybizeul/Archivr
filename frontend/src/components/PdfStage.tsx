import { useEffect, useRef, useState } from 'react'
import { Box, Loader, Text } from '@mantine/core'
import { useElementSize } from '@mantine/hooks'
import * as pdfjs from 'pdfjs-dist'
import type { PDFDocumentProxy, PDFPageProxy, RenderTask } from 'pdfjs-dist'
import pdfWorkerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

pdfjs.GlobalWorkerOptions.workerSrc = pdfWorkerUrl

// The pdf.js files copied into public/pdfjs by scripts/copy-pdfjs-assets.mjs. Without them some
// PDFs draw differently: CJK text, non-embedded fonts, JBIG2 and JPEG 2000 images, and ICC colours.
const factory = `${import.meta.env.BASE_URL}pdfjs/`

/**
 * A PDF drawn page by page with pdf.js, one canvas per page, each as wide as the sheet.
 * The turn is part of each page's viewport, so the canvas comes out already turned. Nothing is
 * rotated with CSS, so the scroll area keeps the screen's axes: wheel and trackpad pan as they do for images.
 */
export function PdfStage({ src, title, turn }: { src: string; title: string; turn: number }) {
  const { ref, width: available } = useElementSize<HTMLDivElement>()
  const [pages, setPages] = useState<PDFPageProxy[] | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    const loading = pdfjs.getDocument({
      url: src,
      cMapUrl: `${factory}cmaps/`,
      cMapPacked: true,
      standardFontDataUrl: `${factory}standard_fonts/`,
      wasmUrl: `${factory}wasm/`,
      iccUrl: `${factory}iccs/`,
    })
    loading.promise
      .then(async (doc: PDFDocumentProxy) => {
        const list = await Promise.all(Array.from({ length: doc.numPages }, (_, i) => doc.getPage(i + 1)))
        if (!cancelled) setPages(list)
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    return () => {
      cancelled = true
      void loading.destroy()
    }
  }, [src])

  if (failed) {
    return (
      <Text c="dimmed" p={12}>
        This PDF could not be shown.
      </Text>
    )
  }

  return (
    <Box className="ax-viewer-image">
      <div ref={ref}>
        {pages === null && <Loader size="sm" m={12} />}
        {pages?.map((page) => (
          <PdfPage
            key={page.pageNumber}
            page={page}
            width={available}
            turn={turn}
            label={`${title}, page ${page.pageNumber} of ${pages.length}`}
          />
        ))}
      </div>
    </Box>
  )
}

/** One PDF page. It is drawn only when it is near the view, so long documents stay light. */
function PdfPage({ page, width, turn, label }: { page: PDFPageProxy; width: number; turn: number; label: string }) {
  const holderRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [near, setNear] = useState(false)

  // The page's own rotation plus the turn. The page is then fitted to the sheet width.
  const rotation = (page.rotate + turn) % 360
  const unit = page.getViewport({ scale: 1, rotation })
  const scale = width > 0 ? width / unit.width : 0
  const shown = { w: unit.width * scale, h: unit.height * scale }

  useEffect(() => {
    const el = holderRef.current
    if (!el || near) return
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) setNear(true)
      },
      { rootMargin: '800px 0px' },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [near])

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || !near || width <= 0) return
    const dpr = window.devicePixelRatio || 1
    const viewport = page.getViewport({ scale: width / page.getViewport({ scale: 1, rotation }).width, rotation })
    canvas.width = Math.floor(viewport.width * dpr)
    canvas.height = Math.floor(viewport.height * dpr)
    const task: RenderTask = page.render({
      canvas,
      viewport,
      transform: dpr === 1 ? undefined : [dpr, 0, 0, dpr, 0, 0],
    })
    // A render that is cancelled because the page changed rejects; that is expected.
    task.promise.catch(() => {})
    return () => task.cancel()
  }, [page, width, rotation, near])

  return (
    <div ref={holderRef} style={{ width: shown.w, height: shown.h, margin: '12px auto 0' }}>
      <canvas
        ref={canvasRef}
        role="img"
        aria-label={label}
        style={{
          display: 'block',
          width: shown.w,
          height: shown.h,
          background: 'var(--ax-sheet)',
          boxShadow: '0 0 0 1px var(--ax-rule)',
        }}
      />
    </div>
  )
}
