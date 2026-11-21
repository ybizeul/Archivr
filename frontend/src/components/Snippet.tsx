import { Fragment } from 'react'

// The server wraps matches in these control characters. They are never HTML.
const START = '\u0001'
const END = '\u0002'

interface Segment {
  text: string
  mark: boolean
}

/**
 * Renders a search snippet. The text is OCR output, so it is shown as plain text
 * and never as HTML; only the match markers become <mark> elements.
 */
export function Snippet({ text }: { text: string }) {
  const segments: Segment[] = []
  let mark = false
  let buf = ''
  for (const ch of text) {
    if (ch === START || ch === END) {
      if (buf) segments.push({ text: buf, mark })
      buf = ''
      mark = ch === START
    } else {
      buf += ch
    }
  }
  if (buf) segments.push({ text: buf, mark })

  return (
    <>
      {segments.map((s, i) =>
        s.mark ? (
          <mark key={i} className="ax-mark">
            {s.text}
          </mark>
        ) : (
          <Fragment key={i}>{s.text}</Fragment>
        ),
      )}
    </>
  )
}
