import { useMemo, useRef, useState } from 'react'
import { ActionIcon, TextInput, type TextInputProps } from '@mantine/core'
import { IconCalendar } from '@tabler/icons-react'
import { datePattern, formatDateInput, parseDateInput } from '../lib/format'

interface DateFieldProps extends Omit<TextInputProps, 'value' | 'onChange' | 'type' | 'rightSection'> {
  /** "YYYY-MM-DD", or "" for no date. */
  value: string
  onValueChange: (iso: string) => void
}

/**
 * A date field that shows and reads dates in the browser's language order (31/10/2021
 * for French, 10/31/2021 for en-US) instead of leaving that to the native date input,
 * which in Safari and Chrome cannot be told which format to use. The value the app
 * stores and sends is always ISO. A calendar button still opens the browser's own picker.
 */
export function DateField({ value, onValueChange, disabled, error, onBlur, ...rest }: DateFieldProps) {
  const pattern = useMemo(() => datePattern(), [])
  // What is on screen while the field has focus; null shows the saved value.
  const [draft, setDraft] = useState<string | null>(null)
  const picker = useRef<HTMLInputElement>(null)

  const text = draft ?? formatDateInput(value, pattern)
  const invalid = draft !== null && draft.trim() !== '' && parseDateInput(draft, pattern) === null

  const openPicker = () => {
    const el = picker.current
    if (!el) return
    if (typeof el.showPicker === 'function') {
      try {
        el.showPicker()
        return
      } catch {
        // Falls through to focusing the field when the browser refuses to open it.
      }
    }
    el.focus()
    el.click()
  }

  return (
    <TextInput
      {...rest}
      value={text}
      disabled={disabled}
      placeholder={rest.placeholder ?? pattern.placeholder}
      // "numeric" gives phones a digit-only pad with no "/" or "-" key.
      inputMode="text"
      autoComplete="off"
      error={error ?? (invalid ? `Use ${pattern.placeholder}` : undefined)}
      onChange={(e) => {
        const next = e.currentTarget.value
        setDraft(next)
        if (next.trim() === '') {
          onValueChange('')
          return
        }
        const iso = parseDateInput(next, pattern)
        if (iso !== null) onValueChange(iso)
      }}
      onBlur={(e) => {
        setDraft(null)
        onBlur?.(e)
      }}
      rightSection={
        // The real date input sits invisibly on top of the icon, so a tap lands on it directly.
        // Mobile Safari and Chrome only open the picker from a genuine tap on a visible,
        // sized date input, not from showPicker() on a hidden zero-size one.
        <span style={{ position: 'relative', display: 'inline-flex', width: 22, height: 22 }}>
          <ActionIcon
            component="span"
            variant="subtle"
            color="gray"
            size="sm"
            disabled={disabled}
            style={{ pointerEvents: 'none' }}
          >
            <IconCalendar size={16} />
          </ActionIcon>
          <input
            ref={picker}
            type="date"
            aria-label="Choose a date"
            value={value}
            disabled={disabled}
            onClick={openPicker}
            onChange={(e) => {
              setDraft(null)
              onValueChange(e.currentTarget.value)
            }}
            style={{
              position: 'absolute',
              inset: 0,
              width: '100%',
              height: '100%',
              opacity: 0,
              cursor: 'pointer',
              border: 0,
              padding: 0,
              fontSize: 16,
            }}
          />
        </span>
      }
      rightSectionPointerEvents="all"
    />
  )
}
