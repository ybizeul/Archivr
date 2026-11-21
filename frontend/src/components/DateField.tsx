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
      inputMode="numeric"
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
        <>
          <ActionIcon
            variant="subtle"
            color="gray"
            size="sm"
            aria-label="Choose a date"
            disabled={disabled}
            // Keeps focus in the text field so its blur does not discard the draft mid-click.
            onMouseDown={(e) => e.preventDefault()}
            onClick={openPicker}
          >
            <IconCalendar size={16} />
          </ActionIcon>
          <input
            ref={picker}
            type="date"
            tabIndex={-1}
            aria-hidden="true"
            value={value}
            disabled={disabled}
            onChange={(e) => {
              setDraft(null)
              onValueChange(e.currentTarget.value)
            }}
            style={{ position: 'absolute', right: 0, bottom: 0, width: 0, height: 0, opacity: 0, pointerEvents: 'none' }}
          />
        </>
      }
      rightSectionPointerEvents="all"
    />
  )
}
