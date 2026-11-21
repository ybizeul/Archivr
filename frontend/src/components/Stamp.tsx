import { useEffect, useState } from 'react'
import { useReducedMotion } from '@mantine/hooks'

interface StampProps {
  /** Change this value to play the stamp again. Zero means never filed, so nothing shows. */
  run: number
  label: string
}

/**
 * The carmine "Filed" stamp. It lands after 300 ms and clears at 2900 ms.
 * Under reduced motion it fades in without the scale-down.
 */
export function Stamp({ run, label }: StampProps) {
  if (run === 0) return null
  // Keyed by run, so every filing starts the stamp from hidden.
  return <StampFace key={run} label={label} />
}

function StampFace({ label }: { label: string }) {
  const reduced = useReducedMotion()
  const [on, setOn] = useState(false)

  useEffect(() => {
    const show = window.setTimeout(() => setOn(true), 300)
    const hide = window.setTimeout(() => setOn(false), 2900)
    return () => {
      window.clearTimeout(show)
      window.clearTimeout(hide)
    }
  }, [])

  const hidden = reduced ? 'rotate(-8deg) scale(1)' : 'rotate(-8deg) scale(1.5)'
  return (
    <div
      className="ax-stamp"
      aria-hidden="true"
      style={{
        opacity: on ? 0.92 : 0,
        transform: on ? 'rotate(-8deg) scale(1)' : hidden,
        transition: reduced
          ? 'opacity 180ms ease-out'
          : 'transform 180ms cubic-bezier(0.2, 0.8, 0.2, 1), opacity 180ms ease-out',
      }}
    >
      {label}
    </div>
  )
}
