import fs from 'node:fs';
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
// 更新期间的恢复由发布器负责；成功后只保留 Git 历史。
console.error(JSON.stringify({ environment: 'prod', ok: false, failures, action: '检查服务日志；必要时从已知 Git 提交重新联调发布' }));
process.exit(1);
