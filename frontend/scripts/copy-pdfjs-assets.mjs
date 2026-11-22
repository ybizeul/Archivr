// Copies the pdf.js files the viewer needs into public/pdfjs, so Vite ships them with the app:
// CMaps (CJK text), standard fonts (base-14 fonts), the WebAssembly decoders for JBIG2 and
// JPEG 2000 images and for ICC colour profiles, and the default CMYK profile.
// Without them pdf.js draws some PDFs differently from the browser's own viewer.
// Runs after npm install; the output is generated, so it is not committed.
import { cpSync, rmSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const source = join(root, 'node_modules', 'pdfjs-dist')
const target = join(root, 'public', 'pdfjs')

rmSync(target, { recursive: true, force: true })
for (const dir of ['cmaps', 'standard_fonts', 'wasm', 'iccs']) {
  cpSync(join(source, dir), join(target, dir), { recursive: true })
}
