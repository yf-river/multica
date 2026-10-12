import { spawnSync } from 'node:child_process';
import path from 'node:path';

// Instance operations share one publisher; this entry cannot seed accounts or start another environment.
const args = process.argv.slice(2);
if (!args.length) args.push('status');
if (args.includes('int')) throw new Error('int 已停用；不会创建环境或映射到生产');
const result = spawnSync('python3', [path.join(import.meta.dirname, 'release-environments.py'), ...args], { stdio: 'inherit' });
process.exit(result.status ?? 1);
