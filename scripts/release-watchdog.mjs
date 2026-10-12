import fs from 'node:fs';
import { spawnSync } from 'node:child_process';
import path from 'node:path';

const root = path.resolve(import.meta.dirname, '..');
const deployment = JSON.parse(fs.readFileSync(path.join(root, '.run/deployments/goal-test-prod.json')));
const failures = [];
try {
  const response = await fetch(deployment.backend_url + '/health');
  if (!response.ok) failures.push(`health ${response.status}`);
} catch (error) { failures.push(`health ${error.message}`); }
for (const [role, pid] of Object.entries(deployment.pids)) {
  try { process.kill(Number(pid), 0); } catch { failures.push(`${role} pid ${pid} missing`); }
}
if (!failures.length) { console.log(JSON.stringify({ environment: 'prod', ok: true, release_id: deployment.release_id })); process.exit(0); }
const result = spawnSync('python3', [path.join(root, 'scripts/release-environments.py'), 'rollback', 'prod'], { cwd: root, encoding: 'utf8' });
console.log(JSON.stringify({ environment: 'prod', ok: result.status === 0, rolled_back: result.status === 0, failures, output: (result.stdout || '').slice(-1000), error: (result.stderr || '').slice(-1000) }));
process.exit(result.status === 0 ? 0 : 1);
