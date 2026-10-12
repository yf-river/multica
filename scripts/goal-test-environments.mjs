import { spawnSync } from 'node:child_process';
import path from 'node:path';

// Instance operations share one publisher; int is isolated from prod.
const args = process.argv.slice(2);
if (!args.length) args.push('status');
const result = spawnSync('python3', [path.join(import.meta.dirname, 'release-environments.py'), ...args], { stdio: 'inherit' });
process.exit(result.status ?? 1);
