import { useState } from 'react'
import { Alert, Button, CopyButton, Group, Modal, NumberInput, Select, Stack, Text, TextInput } from '@mantine/core'
import { IconCheck, IconCopy } from '@tabler/icons-react'
import { useCreateShare } from '../api/queries'
import type { ShareLink } from '../api/types'
import { errorMessage, formatDateTime } from '../lib/format'

type Unit = 'minutes' | 'hours' | 'days'

const UNIT_SECONDS: Record<Unit, number> = { minutes: 60, hours: 3600, days: 86400 }
const MIN_SECONDS = 60
const MAX_SECONDS = 365 * 86400

interface ShareDialogProps {
  opened: boolean
  onClose: () => void
  docId: number
  title: string
}

/**
 * Makes a link anyone can open without signing in, for as long as the user chooses.
 * The link is a signed token that holds its own expiry. Nothing is stored on the server.
 */
export function ShareDialog({ opened, onClose, docId, title }: ShareDialogProps) {
  const create = useCreateShare()
  const [amount, setAmount] = useState<number | string>(7)
  const [unit, setUnit] = useState<Unit>('days')
  const [link, setLink] = useState<ShareLink | null>(null)

  const seconds = typeof amount === 'number' ? Math.round(amount * UNIT_SECONDS[unit]) : 0
  const error =
    seconds < MIN_SECONDS
      ? 'Enter at least 1 minute'
      : seconds > MAX_SECONDS
        ? 'Links can stay valid for 365 days at most'
        : undefined

  const close = () => {
    onClose()
    // Start clean next time, so an old link is not shown for a new choice.
    setLink(null)
    create.reset()
  }

  return (
    <Modal opened={opened} onClose={close} title={`Share “${title}”`} centered>
      <Stack>
        <Text size="sm">
          Anyone with the link can view and download this document, without signing in. They see nothing else in
          the archive.
        </Text>

        <Group align="flex-start" wrap="nowrap" gap="sm">
          <NumberInput
            label="Link is valid for"
            value={amount}
            onChange={(v) => {
              setAmount(v)
              setLink(null)
            }}
            min={1}
            allowDecimal={false}
            allowNegative={false}
            error={error}
            style={{ flex: 1 }}
          />
          <Select
            label=" "
            aria-label="Unit"
            data={[
              { value: 'minutes', label: 'minutes' },
              { value: 'hours', label: 'hours' },
              { value: 'days', label: 'days' },
            ]}
            value={unit}
            onChange={(v) => {
              if (v) setUnit(v as Unit)
              setLink(null)
            }}
            allowDeselect={false}
            w={130}
          />
        </Group>

        {create.isError && (
          <Alert color="oxide" variant="light" title="Couldn't make the link">
            {errorMessage(create.error)}
          </Alert>
        )}

        {link ? (
          <Stack gap="xs">
            <TextInput
              label="Share link"
              value={link.url}
              readOnly
              onFocus={(e) => e.currentTarget.select()}
              rightSectionWidth={96}
              rightSection={
                <CopyButton value={link.url}>
                  {({ copied, copy }) => (
                    <Button
                      size="compact-sm"
                      variant="light"
                      onClick={copy}
                      leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                    >
                      {copied ? 'Copied' : 'Copy'}
                    </Button>
                  )}
                </CopyButton>
              }
            />
            <Text size="sm" c="dimmed">
              Works until {formatDateTime(link.expires_at)}. It cannot be cancelled before then, except by deleting
              the document.
            </Text>
          </Stack>
        ) : null}

        <Group justify="flex-end">
          <Button variant="default" onClick={close}>
            Close
          </Button>
          <Button
            disabled={!!error}
            loading={create.isPending}
            onClick={() =>
              create.mutate({ id: docId, expiresInSeconds: seconds }, { onSuccess: (res) => setLink(res) })
            }
          >
            {link ? 'Make a new link' : 'Create link'}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
