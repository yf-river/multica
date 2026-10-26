import { readFile, realpath } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'

export interface RuntimeHost {
  kind: 'node-package'
  node: string
  entrypoint: string
  package_root: string
  version: string
}

// 只报告进程的启动身份，不包含 argv、环境变量或用户配置。
export async function runtimeHost(): Promise<RuntimeHost | undefined> {
  if (process.versions.electron !== undefined || process.argv[1] === undefined) return undefined
  try {
    const entrypoint = await realpath(process.argv[1])
    let directory = dirname(entrypoint)
    for (;;) {
      try {
        const pkg = JSON.parse(await readFile(resolve(directory, 'package.json'), 'utf8')) as { name?: string; version?: string }
        if (pkg.name === '@deepseek-ai/dsh' && typeof pkg.version === 'string') {
          return { kind: 'node-package', node: await realpath(process.execPath), entrypoint, package_root: directory, version: pkg.version }
        }
      } catch { /* 父目录可能没有包元数据。 */ }
      const parent = dirname(directory)
      if (parent === directory) return undefined
      directory = parent
    }
  } catch { return undefined }
}
