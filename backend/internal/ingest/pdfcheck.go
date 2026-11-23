package ingest

import (
	"bytes"
)

// eofWindow is how far from the end of a PDF the end-of-file marker is searched.
// Trailing junk after %%EOF is tolerated, as some writers append it.
const eofWindow = 64 * 1024

// IncompletePDF returns why data is a PDF that was cut short, or "" when it is
// not a PDF or looks whole. Every complete PDF ends with a %%EOF marker. A copy
// that stopped early does not have one, and most readers cannot open it.
func IncompletePDF(data []byte) string {
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return ""
	}
	tail := data
	if len(tail) > eofWindow {
		tail = tail[len(tail)-eofWindow:]
	}
	if bytes.Contains(tail, []byte("%%EOF")) {
		return ""
	}
	return "the PDF has no end-of-file marker, so the copy is incomplete"
}
