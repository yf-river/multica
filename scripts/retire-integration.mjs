import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import pg from 'pg';
import { repoRoot, loadProductionDeployment } from './lib/goal-test-audit-env.mjs';

// One-time retirement reads both live ownership records; daily release commands never use int.
const runRoot = path.join(repoRoot, '.run');
const intMetadataPath = path.join(runRoot, 'deployments/goal-test-int.json');
const intConfigPath = path.join(runRoot, 'env/int-services.json');
if (!fs.existsSync(intMetadataPath) || !fs.existsSync(intConfigPath)) throw Error('int ownership records missing; no resources removed');
const int = JSON.parse(fs.readFileSync(intMetadataPath));
const prod = loadProductionDeployment();
const intConfig = JSON.parse(fs.readFileSync(intConfigPath));
const prodConfig = JSON.parse(fs.readFileSync(prod.runtime_config_file));
const intDb = new URL(intConfig.server.DATABASE_URL);
const prodDb = new URL(prodConfig.server.DATABASE_URL);
const dbIdentity = u => `${u.hostname.replace('localhost', '127.0.0.1')}:${u.port || '5432'}${u.pathname}`;
if (int.environment !== 'int' || intConfig.server.MULTICA_ENV_ID !== 'int' || dbIdentity(intDb) === dbIdentity(prodDb)) throw Error('database ownership is not exclusively int');
const database = decodeURIComponent(intDb.pathname.slice(1));
if (database !== int.database_name || !/^[a-zA-Z0-9_]+$/.test(database)) throw Error('database metadata mismatch');
const targets = [
  path.join(runRoot, 'releases/int'), path.join(runRoot, 'state/int'),
  intConfig.server.LOCAL_UPLOAD_DIR, int.daemon_workspaces_root, path.dirname(int.daemon_profile_path),
  path.join(runRoot, 'log-archive/int'),
  intMetadataPath, intConfigPath, path.join(runRoot, 'deployments/int-previous.json'),
  path.join(runRoot, 'env/goal-test-int.env'), path.join(runRoot, 'env/int-before-deploy.json'),
  path.join(runRoot, 'env/int-previous-server.json'),
  ...['server', 'web', 'daemon'].flatMap(role => [path.join(runRoot, `int-${role}.pid`), path.join(runRoot, `int-${role}.log`)]),
].filter(file => fs.existsSync(file));
const protectedPaths = [prod.release_dir, path.dirname(prod.daemon_profile_path), prod.daemon_workspaces_root,
  prodConfig.server.LOCAL_UPLOAD_DIR, prodConfig.daemon.CODEBUDDY_CONFIG_DIR, path.join(runRoot, 'artifacts'),
  path.join(runRoot, 'releases/prod'), path.join(runRoot, 'backups')].map(file => fs.realpathSync(file));
for (const file of targets) {
  const resolved = fs.realpathSync(file);
  if (resolved !== path.resolve(file) || [repoRoot, runRoot, '/', '/root'].includes(resolved)) throw Error(`unsafe deletion target: ${file}`);
  if (protectedPaths.some(p => p === resolved || p.startsWith(resolved + '/') || resolved.startsWith(p + '/'))) throw Error(`shared production target: ${file}`);
  if (!(resolved.startsWith(runRoot + '/') || resolved === path.dirname(int.daemon_profile_path))) throw Error(`outside int resource roots: ${file}`);
}
const command = (cmd, args) => {
  const result = spawnSync(cmd, args, { encoding: 'utf8' });
  if (result.status !== 0) throw Error(`${cmd} failed: ${(result.stderr || result.stdout).slice(0, 500)}`);
  return result.stdout.trim();
};
const repos = command('find', [int.daemon_workspaces_root, '-name', '.git', '-prune']).split('\n').filter(Boolean);
for (const gitDir of repos) {
  const repo = path.dirname(gitDir);
  if (command('git', ['-C', repo, 'status', '--porcelain'])) throw Error(`int task directory contains unique uncommitted work: ${repo}`);
}
const plan = { database, shared_database_role_preserved: intDb.username, paths: targets, installed_packages_preserved: ['production current and previous artifacts', 'shared build snapshots'] };
console.log(JSON.stringify(plan));
if (!process.argv.includes('--execute')) process.exit(0);
await import('node:fs/promises').then(async ({ access }) => access(path.join(runRoot, 'evidence/production-release', prod.release_id, 'browser.json')));
const stop = spawnSync('python3', ['-c', [
  'import importlib.util',
  `s=importlib.util.spec_from_file_location("releases",${JSON.stringify(path.join(repoRoot, 'scripts/release-environments.py'))})`,
  'r=importlib.util.module_from_spec(s);s.loader.exec_module(r)',
  'with r.lock("int"):',
  ' m=r.read(r.meta_path("int"));r.check_process_ownership("int",m)',
  ' r.drain("int",m)',
  ' r.http(r.health_url("int")+"/shutdown","POST")',
  ' deadline=r.time.monotonic()+60',
  ' while r.exists(m["pids"]["daemon"]) and r.time.monotonic()<deadline:r.time.sleep(.2)',
  ' if r.exists(m["pids"]["daemon"]):raise RuntimeError("int daemon did not exit; no resources deleted")',
  ' r.owned_stop(m["pids"]["web"],"web",r.Path(m["release_dir"])/"apps/web")',
  ' r.owned_stop(m["pids"]["server"],"server",m["release_dir"])',
].join('\n')], { stdio: 'inherit' });
if (stop.status !== 0) throw Error('int stop failed; database and directories preserved');
const unit = 'multica-goal-test-int.service';
const unitPath = command('systemctl', ['show', unit, '-p', 'FragmentPath', '--value']);
if (unitPath) {
  if (!fs.readFileSync(unitPath, 'utf8').includes('deploy int')) throw Error('int startup unit ownership unclear');
  const group = command('systemctl', ['show', unit, '-p', 'ControlGroup', '--value']);
  const processes = path.join('/sys/fs/cgroup', group, 'cgroup.procs');
  if (fs.existsSync(processes) && fs.readFileSync(processes, 'utf8').trim()) throw Error('old int startup unit still owns processes; no unit or data deleted');
  command('systemctl', ['disable', unit]);
  command('systemctl', ['stop', unit]);
  fs.unlinkSync(unitPath);
  command('systemctl', ['daemon-reload']);
}
const adminUrl = new URL(intDb); adminUrl.pathname = '/postgres';
const db = new pg.Client({ connectionString: adminUrl.href });
await db.connect();
try {
  const existing = await db.query('SELECT datname, pg_get_userbyid(datdba) AS owner FROM pg_database WHERE datname=$1', [database]);
  if (existing.rows.length !== 1 || existing.rows[0].owner !== decodeURIComponent(intDb.username)) throw Error('database owner mismatch');
  const connections = await db.query('SELECT pid FROM pg_stat_activity WHERE datname=$1', [database]);
  if (connections.rows.length) throw Error('int database still has clients; no force disconnect or deletion');
  await db.query(`DROP DATABASE "${database}"`);
} finally { await db.end(); }
for (const file of targets) fs.rmSync(file, { recursive: fs.statSync(file).isDirectory(), force: false });
const evidence = { ...plan, deleted_at: new Date().toISOString(), status: 'deleted', irreversible: true };
fs.mkdirSync(path.join(runRoot, 'evidence/int-retirement'), { recursive: true });
fs.writeFileSync(path.join(runRoot, 'evidence/int-retirement/result.json'), JSON.stringify(evidence, null, 2), { mode: 0o600 });
console.log(JSON.stringify({ status: 'deleted', database, removed_paths: targets.length }));
