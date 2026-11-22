import { Input, TagsInput, type TagsInputProps } from '@mantine/core'

// TagsInput whose clear button just empties the tags. Mantine's built-in
// clearable button also focuses the input and opens the suggestion menu, which
// is unwanted when the user is only discarding the tags.
export function TagsField({ clearable, value, onChange, disabled, readOnly, ...props }: TagsInputProps) {
  const showClear = clearable && !disabled && !readOnly && (value?.length ?? 0) > 0
  return (
    <TagsInput
      {...props}
      value={value}
      onChange={onChange}
      disabled={disabled}
      readOnly={readOnly}
      rightSection={
        showClear ? (
          <Input.ClearButton
            tabIndex={-1}
            aria-label="Clear tags"
            // Keep focus where it is, so clicking never focuses the field.
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => onChange?.([])}
          />
        ) : (
          props.rightSection
        )
      }
    />
  )
}
