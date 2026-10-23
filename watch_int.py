import json, subprocess, time, os, sys

OURS = '01a16791-79c5-78db-810a-5ca10d2d29aa'
INTCLI = '/tmp/gong60-intcli'
PROBE = 'GONG-76'

def dep():
    try:
        return json.load(open('/data/ida/multica/.run/deployments/goal-test-int.json'))
    except Exception:
        return {}

def cand():
    try:
        return json.load(open('/data/ida/multica/.run/candidate.json'))
    except Exception:
        return {}

def cli(args, timeout=60):
    try:
        out = subprocess.run(['multica'] + args, cwd=INTCLI, capture_output=True, text=True, timeout=timeout)
        return out.stdout
    except Exception as e:
        return 'ERR ' + str(e)

deadline = time.time() + 1500
live = False
while time.time() < deadline:
    c = cand(); d = dep()
    if c.get('delivery_id') == OURS and c.get('status') in ('verifying', 'accepted'):
        live = True
        print('LIVE', time.strftime('%H:%M:%S'), d.get('commit', '')[:12], c.get('status'), flush=True)
        break
    time.sleep(3)

if not live:
    print('NO_WINDOW', json.dumps({'dep': dep().get('commit'), 'cand': cand().get('delivery_id')}), flush=True)
    sys.exit(0)

# 立即启动探针任务
print('START_PROBE', cli(['issue', 'status', PROBE, 'todo', '--no-start'])[:200], flush=True)

run_seen = None
for _ in range(120):
    out = cli(['issue', 'runs', PROBE, '--output', 'json'])
    try:
        data = json.loads(out)
    except Exception:
        data = None
    runs = data if isinstance(data, list) else (data or {}).get('runs', [])
    if runs:
        r = runs[0]
        st = r.get('status')
        if st != run_seen:
            print('RUN', time.strftime('%H:%M:%S'), st, r.get('id'), flush=True)
            run_seen = st
        if st in ('completed', 'failed', 'cancelled'):
            print('RUN_DONE', st, json.dumps(r)[:400], flush=True)
            break
    time.sleep(5)
print('DONE', time.strftime('%H:%M:%S'), flush=True)
