import fs from 'node:fs';
import { spawnSync } from 'node:child_process';
import path from 'node:path';

const root = path.resolve(import.meta.dirname, '..');
const config = JSON.parse(fs.readFileSync(path.join(root, '.run/env/int-services.json')));
if (config.server.MULTICA_ENV_ID !== 'int') throw Error('拒绝重置非 int 环境');
const url = new URL(config.server.DATABASE_URL);
const db = decodeURIComponent(url.pathname.slice(1));
if (db !== 'multica_goal_test_int') throw Error('int 数据库名称不匹配');
const container = spawnSync('docker', ['ps', '--filter', `publish=${url.port || 5432}`, '--format', '{{.ID}}'], { encoding: 'utf8' }).stdout.trim().split(/\s+/)[0];
if (!container) throw Error('找不到 int PostgreSQL 容器');
const run = args => { const r = spawnSync('docker', ['exec', container, ...args], { encoding: 'utf8' }); if (r.status !== 0) throw Error((r.stderr || r.stdout).trim()); };
run(['psql', '-U', 'multica', '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-c', `DROP DATABASE IF EXISTS ${db} WITH (FORCE);`, '-c', `CREATE DATABASE ${db} WITH TEMPLATE multica_goal_test_int_seed;`]);
console.log(JSON.stringify({ environment: 'int', database: db, status: 'reset', seed: 'multica_goal_test_int_seed' }));
