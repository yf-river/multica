import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { resolve, join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const destination = resolve(process.argv[2] ?? join(root, 'artifacts'))
const pkg = JSON.parse(await readFile(join(root, 'package.json'), 'utf8'))
const compat = JSON.parse(await readFile(join(root, 'compat.json'), 'utf8'))
const { BRIDGE_VERSION } = await import('../dist/version.js')
if (pkg.version !== compat.bridgeVersion || pkg.version !== BRIDGE_VERSION) {
  throw new Error('bridge version differs between package, compatibility manifest and build')
}
await mkdir(destination, { recursive: true })
const staging = await mkdtemp(join(destination, '.pack-'))
try {
  const result = spawnSync('pnpm', ['pack', '--pack-destination', staging], {
    cwd: root, encoding: 'utf8', shell: process.platform === 'win32',
  })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`bridge pack failed: ${result.stderr}\n${result.stdout}`)
  const packed = join(staging, `multica-ai-dsh-runtime-${pkg.version}.tgz`)
  const bytes = await readFile(packed)
  const filename = `dsh-runtime-${pkg.version}.tgz`
  const manifest = {
    ...compat, filename, sha256: createHash('sha256').update(bytes).digest('hex'), bytes: bytes.length,
  }
  await writeFile(join(staging, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  await rename(packed, join(destination, filename))
  await rename(join(staging, 'manifest.json'), join(destination, 'manifest.json'))
  process.stdout.write(JSON.stringify(manifest) + '\n')
} finally {
  await rm(staging, { recursive: true, force: true })
}
