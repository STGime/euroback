// Adds `.js` extensions to relative imports inside dist/ (the ESM build):
// Node's ESM resolver needs them, and so does TypeScript with
// moduleResolution node16 / nodenext for the .d.ts files. dist/cjs (the
// CommonJS build) is a subfolder and isn't touched.

import { readdir, readFile, writeFile } from 'node:fs/promises'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const distDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist')

const RELATIVE_IMPORT = /(from\s+['"])(\.\/[A-Za-z0-9_\-]+)(['"])/g
const RELATIVE_TYPE_IMPORT = /(import\(['"])(\.\/[A-Za-z0-9_\-]+)(['"]\))/g

for (const entry of await readdir(distDir)) {
  if (!entry.endsWith('.js') && !entry.endsWith('.d.ts')) continue
  const path = join(distDir, entry)
  const src = await readFile(path, 'utf8')
  const out = src.replace(RELATIVE_IMPORT, '$1$2.js$3').replace(RELATIVE_TYPE_IMPORT, '$1$2.js$3')
  if (out !== src) await writeFile(path, out)
}
