import { useState } from 'react'
import { Alert, Badge, Button, Group, Loader, NumberInput, Stack, Switch, Text, TextInput } from '@mantine/core'
import { notifications } from '@mantine/notifications'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { keys, useOCRSettings } from '../../api/queries'
import type { OCRSettings } from '../../api/types'
import { errorMessage } from '../../lib/format'

export function OCRSettingsPanel() {
  const settings = useOCRSettings()
  if (settings.isPending) return <Loader size="sm" />
  if (!settings.data) return <Text c="dimmed">The OCR settings could not be loaded.</Text>
  return <OCRForm key={JSON.stringify(settings.data)} initial={settings.data} />
}

function OCRForm({ initial }: { initial: OCRSettings }) {
  const qc = useQueryClient()
  const [enabled, setEnabled] = useState(initial.enabled)
  const [languages, setLanguages] = useState(initial.languages)
  const [timeout, setTimeoutSeconds] = useState<number | string>(initial.timeout_seconds)

  const save = useMutation({
    mutationFn: () =>
      api.put<OCRSettings>('/admin/settings/ocr', {
        enabled,
        languages: languages.trim(),
        timeout_seconds: Number(timeout) || 300,
      }),
    onSuccess: () => {
      notifications.show({ color: 'prussian', message: 'OCR settings saved' })
      void qc.invalidateQueries({ queryKey: keys.adminOCR })
    },
    onError: (err) => notifications.show({ color: 'red', title: "Couldn't save", message: errorMessage(err) }),
  })

  const runMissing = useMutation({
    mutationFn: () => api.post<{ queued: number }>('/admin/ocr/run-missing'),
    onSuccess: ({ queued }) => {
      notifications.show({
        color: 'prussian',
        message: queued === 0 ? 'No documents are missing OCR' : `OCR queued for ${queued} document${queued === 1 ? '' : 's'}`,
      })
      void qc.invalidateQueries({ queryKey: keys.adminOCR })
    },
    onError: (err) => notifications.show({ color: 'red', title: "Couldn't start OCR", message: errorMessage(err) }),
  })

  return (
    <Stack maw={620} gap="md">
      <Stack gap={6}>
        <Text fw={600}>This machine</Text>
        <Group gap="xs">
          <Badge variant="outline" color={initial.tesseract ? 'prussian' : 'oxide'} radius="sm">
            {initial.tesseract ? `Tesseract ${initial.tesseract_version}` : 'Tesseract not found'}
          </Badge>
          <Badge variant="outline" color={initial.poppler ? 'prussian' : 'gray'} radius="sm">
            {initial.poppler ? 'Poppler found' : 'Poppler not found'}
          </Badge>
        </Group>
        <Text size="sm" c="dimmed">
          {initial.installed_languages.length} language{initial.installed_languages.length === 1 ? '' : 's'} installed. The
          ones in use are checked below.
        </Text>
        <Group gap={6}>
          {initial.languages
            .split('+')
            .filter(Boolean)
            .map((code) => {
              const ok = initial.installed_languages.includes(code)
              return (
                <Badge key={code} variant="outline" color={ok ? 'prussian' : 'oxide'} radius="sm" className="ax-mono">
                  {code} {ok ? '' : '· missing'}
                </Badge>
              )
            })}
        </Group>
        {!initial.tesseract && (
          <Alert color="oxide" variant="light" title="OCR is off">
            Install Tesseract to read text from scans. Archivr keeps the files either way.
          </Alert>
        )}
        {initial.missing_languages.length > 0 && (
          <Alert color="oxide" variant="light" title="Missing language data">
            Install the Tesseract data for {initial.missing_languages.join(', ')}, or remove it from the list below.
          </Alert>
        )}
        {!initial.poppler && (
          <Text size="xs" c="dimmed">
            Without poppler-utils, scanned PDFs get no text and no preview.
          </Text>
        )}
      </Stack>

      <Switch label="Read text from new documents" checked={enabled} onChange={(e) => setEnabled(e.currentTarget.checked)} />
      <TextInput
        label="Languages"
        description="Tesseract codes joined with +, for example eng+fra."
        value={languages}
        onChange={(e) => setLanguages(e.currentTarget.value)}
        error={languages && !/^[A-Za-z_]+(\+[A-Za-z_]+)*$/.test(languages.trim()) ? 'Use codes like eng+fra' : undefined}
      />
      <NumberInput
        label="Time limit per document (seconds)"
        description="Between 30 and 1800 seconds."
        value={timeout}
        onChange={setTimeoutSeconds}
        min={30}
        max={1800}
        w={260}
      />
      <Group justify="flex-end">
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save OCR settings
        </Button>
      </Group>

      <Stack gap={6}>
        <Text fw={600}>Documents without text</Text>
        <Text size="sm" c="dimmed">
          {initial.documents_missing_ocr === 0
            ? 'Every document has OCR text.'
            : `${initial.documents_missing_ocr} document${initial.documents_missing_ocr === 1 ? ' has' : 's have'} no OCR text: OCR failed, was skipped, or found nothing.`}{' '}
          Save your settings first, then run OCR on them.
        </Text>
        <Group>
          <Button
            variant="default"
            onClick={() => runMissing.mutate()}
            loading={runMissing.isPending}
            disabled={initial.documents_missing_ocr === 0 || !initial.tesseract}
          >
            Run OCR on documents without text
          </Button>
        </Group>
      </Stack>
    </Stack>
  )
}
