import { useParams } from 'react-router-dom'
import { Box, Button, Center, Group, Loader, Stack, Text, Title } from '@mantine/core'
import { IconDownload } from '@tabler/icons-react'
import { useSharedDocument } from '../api/queries'
import { ApiError } from '../api/client'
import { DocViewer } from '../components/DocViewer'
import { formatBytes, formatDate, formatDateTime } from '../lib/format'

/**
 * The public page behind a share link. It needs no sign-in: the signed token in the address is
 * the credential. It shows one document, with a download, and links nowhere else in the app.
 */
export function SharePage() {
  const { token } = useParams()
  const doc = useSharedDocument(token)

  if (doc.isPending) {
    return (
      <Center mih="100vh">
        <Loader size="sm" />
      </Center>
    )
  }
  if (doc.isError || !doc.data) {
    const expired = doc.error instanceof ApiError && doc.error.code === 'SHARE_EXPIRED'
    return (
      <Center mih="100vh" p="md">
        <Stack maw={420} gap="xs">
          <Title order={3}>{expired ? 'This link has expired' : 'This link does not work'}</Title>
          <Text c="dimmed">
            {expired
              ? 'Ask the person who sent it for a new link.'
              : 'It may be incomplete, or the document may have been removed. Check the link, or ask the sender for a new one.'}
          </Text>
        </Stack>
      </Center>
    )
  }

  const base = `/api/v1/shares/${encodeURIComponent(token ?? '')}/file`
  const d = doc.data
  return (
    <Box className="ax-detail">
      <Group justify="space-between" align="flex-start" mb="md" wrap="nowrap">
        <Stack gap={2} style={{ minWidth: 0 }}>
          <Text className="ax-mono ax-accent" size="xs" tt="uppercase">
            Shared document
          </Text>
          <Title order={3} style={{ overflowWrap: 'anywhere' }}>
            {d.title}
          </Title>
          <Text size="sm" c="dimmed">
            {[d.document_date ? formatDate(d.document_date) : null, formatBytes(d.size), `Link valid until ${formatDateTime(d.expires_at)}`]
              .filter(Boolean)
              .join(' · ')}
          </Text>
        </Stack>
        <Button
          component="a"
          href={`${base}?download=1`}
          leftSection={<IconDownload size={16} stroke={1.8} />}
        >
          Download
        </Button>
      </Group>

      <Box className="ax-sheet ax-share-sheet">
        <DocViewer src={base} mime={d.mime} title={d.title} rotation={d.rotation} readOnly />
      </Box>
    </Box>
  )
}
