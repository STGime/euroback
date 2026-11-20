// dist/cjs holds the CommonJS build (tsconfig.cjs.json). The package is
// "type": "module", so this nested package.json tells Node (and
// TypeScript) that the .js and .d.ts files in dist/cjs are CommonJS.

import { writeFile } from 'node:fs/promises'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const cjsDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist', 'cjs')
await writeFile(join(cjsDir, 'package.json'), JSON.stringify({ type: 'commonjs' }, null, 2) + '\n')
