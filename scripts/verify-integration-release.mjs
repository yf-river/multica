import fs from 'node:fs';
import path from 'node:path';

const root = path.resolve(import.meta.dirname, '..');
const deployment = JSON.parse(fs.readFileSync(path.join(root, '.run/deployments/goal-test-int.json')));
const releaseId = process.argv[2] || deployment.release_id;
if (deployment.release_id !== releaseId) throw Error(`int 未运行候选 release: ${releaseId}`);
const evidencePath = path.join(root, '.run/evidence/int-release', releaseId, 'browser.json');
if (!fs.existsSync(evidencePath)) throw Error('缺少 int 浏览器验收证据');
const browser = JSON.parse(fs.readFileSync(evidencePath));
if (browser.environment !== 'int' || browser.release_id !== releaseId || browser.page_errors || browser.failed_requests) {
  throw Error('int 浏览器证据不完整或对应错误版本');
}
const logs = Object.values(deployment.log_paths).map(file => fs.existsSync(file) ? fs.readFileSync(file, 'utf8') : '');
const fatal = logs.flatMap(log => log.split(/\r?\n/)).filter(line => /panic:|fatal error|UnhandledPromiseRejection|EADDRINUSE|Cannot find module/.test(line));
if (fatal.length) throw Error(`int 存在致命日志: ${fatal.slice(0, 3).join(' | ')}`);
const result = {
  release_id: releaseId,
  environment: 'int',
  task_ids: [process.env.MULTICA_REQUESTED_TASK_ID || ''].filter(Boolean),
  task_completed: process.env.MULTICA_TASK_COMPLETED === '1',
  browser,
  fatal_errors: fatal.length,
  promotion_eligible: process.env.MULTICA_TASK_COMPLETED === '1' && Boolean(process.env.MULTICA_REQUESTED_TASK_ID),
  verified_at: new Date().toISOString(),
};
if (!result.task_completed) throw Error('任务尚未自然完成，禁止提升生产');
if (!result.task_ids.length) throw Error('缺少发布请求对应的任务 ID');
const directory = path.dirname(evidencePath);
fs.writeFileSync(path.join(directory, 'promotion.json'), JSON.stringify(result, null, 2), { mode: 0o600 });
console.log(JSON.stringify(result));
