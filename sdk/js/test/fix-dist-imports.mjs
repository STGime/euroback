// Adds `.js` extensions to relative imports inside dist/ so the test harness
// can load the ESM build directly under Node's strict ESM resolver.
// Build consumers (bundlers / TypeScript) tolerate extensionless imports;
// raw Node does not.

import { readdir, readFile, writeFile } from 'node:fs/promises'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const distDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist')

const RELATIVE_IMPORT = /(from\s+['"])(\.\/[A-Za-z0-9_\-]+)(['"])/g

for (const entry of await readdir(distDir)) {
  if (!entry.endsWith('.js')) continue
  const path = join(distDir, entry)
  const src = await readFile(path, 'utf8')
  const out = src.replace(RELATIVE_IMPORT, '$1$2.js$3')
  if (out !== src) await writeFile(path, out)
}
