#!/usr/bin/env python3
"""迁移已安装的 .multica-install 技能包；所有变更通过工作区鉴权 API 完成。"""
import argparse, base64, hashlib, io, json, os, pathlib, urllib.request, uuid, zipfile


def digest(data):
    return hashlib.sha256(data).hexdigest()


def binding_state(skills):
    # 技能内容更新会改变摘要时间；绑定只比较技能身份和启用状态。
    return sorted((s['id'], s.get('enabled', True)) for s in skills)


def safe_path(name):
    p = pathlib.PurePosixPath(name)
    if not name or p.is_absolute() or '..' in p.parts or '\\' in name or '\0' in name or str(p) != name:
        raise ValueError('非法文件路径: ' + name)
    return name


def unpack(record):
    files = {f['path']: f.get('content', '') for f in record['files']}
    spec = json.loads(files['.multica-install/bundle.json'])
    result = {}
    for f in spec['files']:
        name = safe_path(f['path'])
        if name in result:
            raise ValueError('重复路径: ' + name)
        value = files[f['stored_path']]
        data = base64.b64decode(value, validate=True) if f['encoding'] == 'base64' else value.encode()
        if digest(data) != f['sha256']:
            raise ValueError('旧包校验失败: ' + name)
        result[name] = (data, 0o755 if f['mode'] & 0o111 else 0o644)
    if 'SKILL.md' not in result:
        raise ValueError('缺少原始 SKILL.md')
    if len(result)-1 > 512 or sum(len(v[0]) for k,v in result.items() if k != 'SKILL.md') > 64 << 20:
        raise ValueError('资源包超过限制')
    if any(len(v[0]) > 16 << 20 for v in result.values()):
        raise ValueError('单文件超过限制')
    return result


def archive(files):
    out = io.BytesIO()
    with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED) as z:
        for name, (data, mode) in sorted(files.items()):
            info = zipfile.ZipInfo(name); info.create_system = 3; info.external_attr = (0o100000 | mode) << 16
            z.writestr(info, data)
    return out.getvalue()


class API:
    def __init__(self, profile, workspace):
        cfg = json.loads(pathlib.Path(profile).read_text())
        self.base = cfg['server_url'].rstrip('/')
        self.headers = {'Authorization': 'Bearer ' + cfg['token'], 'X-Workspace-ID': workspace}
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def call(self, path, method='GET', body=None, content_type='application/json', raw=False):
        if isinstance(body, dict):
            body = json.dumps(body, ensure_ascii=False).encode()
        req = urllib.request.Request(self.base + path, data=body, method=method,
                                     headers={**self.headers, 'Content-Type': content_type})
        with self.opener.open(req, timeout=180) as response:
            data = response.read()
            return data if raw else json.loads(data) if data else None

    def replace(self, skill_id, files):
        boundary = 'multica-' + uuid.uuid4().hex
        data = archive(files)
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="skill.zip"\r\n'
                'Content-Type: application/zip\r\n\r\n').encode() + data + f'\r\n--{boundary}--\r\n'.encode()
        return self.call(f'/api/skills/{skill_id}/bundle', 'PUT', body, f'multipart/form-data; boundary={boundary}')


def verify(api, old, expected):
    new = api.call('/api/skills/' + old['id'])
    for key in ('id', 'name', 'description', 'config', 'created_by'):
        if new[key] != old[key]:
            raise ValueError('技能属性发生变化: ' + key)
    actual = {'SKILL.md': (new['content'].encode(), 0o644)}
    for f in new['files']:
        data = api.call(f"/api/skills/{new['id']}/files/{f['id']}/content", raw=True) if f.get('kind') == 'binary' else f['content'].encode()
        actual[f['path']] = (data, f.get('mode', 0o644))
    if set(actual) != set(expected):
        raise ValueError('文件集合不一致: ' + str(set(actual) ^ set(expected)))
    for name, (data, mode) in expected.items():
        if actual[name][0] != data or (name != 'SKILL.md' and actual[name][1] != mode):
            raise ValueError('文件内容或权限不一致: ' + name)
    return {'id': old['id'], 'name': old['name'], 'files': len(expected), 'verified': True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['dry-run', 'execute', 'verify', 'restore'])
    parser.add_argument('--profile', required=True)
    parser.add_argument('--workspace', required=True)
    parser.add_argument('--inventory', help='已安装技能的 id 列表 JSON（每项包含 id）')
    parser.add_argument('--backup', required=True, help='本次迁移专用备份目录；execute 使用 dry-run 的快照')
    args = parser.parse_args()
    api = API(args.profile, args.workspace)
    backup = pathlib.Path(args.backup)
    if args.action == 'dry-run':
        if not args.inventory:
            parser.error('dry-run requires --inventory')
        backup.mkdir(parents=True, exist_ok=False);backup.chmod(0o700)
        inventory = json.loads(pathlib.Path(args.inventory).read_text())
        snapshots = []
        for row in inventory:
            record = api.call('/api/skills/' + row['id'])
            files = unpack(record)
            snapshots.append(record)
            print(json.dumps({'name':record['name'],'files':len(files),'bytes':sum(len(v[0]) for v in files.values())},ensure_ascii=False))
        (backup/'skills.json').write_text(json.dumps(snapshots,ensure_ascii=False));(backup/'skills.json').chmod(0o600)
        agents=api.call('/api/agents')
        if isinstance(agents,dict):agents=agents.get('agents',[])
        bindings={a['id']:api.call(f"/api/agents/{a['id']}/skills") for a in agents}
        (backup/'bindings.json').write_text(json.dumps(bindings));(backup/'bindings.json').chmod(0o600)
        print('dry-run complete:',len(snapshots));return
    snapshots = json.loads((backup/'skills.json').read_text())
    results = []
    for old in snapshots:
        if args.action == 'restore':
            payload = {k: old[k] for k in ('name','description','content','config')}
            payload['files']=[{'path':f['path'],'content':f['content']} for f in old['files']]
            api.call('/api/skills/' + old['id'], 'PUT', payload)
            check=api.call('/api/skills/'+old['id'])
            if check['content']!=old['content'] or {f['path']:f['content'] for f in check['files']}!={f['path']:f['content'] for f in old['files']}:
                raise ValueError('恢复校验失败: '+old['name'])
            results.append({'id':old['id'],'restored':True});continue
        files = unpack(old)
        if args.action == 'execute':
            current=api.call('/api/skills/'+old['id'])
            if any(f['path']=='.multica-install/bundle.json' for f in current['files']):
                if current['updated_at']!=old['updated_at']:
                    raise ValueError('备份后技能已更新，请重新 dry-run: '+old['name'])
                api.replace(old['id'], files)
        results.append(verify(api, old, files))
    bindings=json.loads((backup/'bindings.json').read_text())
    for agent_id,skills in bindings.items():
        if binding_state(api.call(f'/api/agents/{agent_id}/skills'))!=binding_state(skills):
            raise ValueError('智能体绑定发生变化: '+agent_id)
    report=backup/(args.action+'-result.json');report.write_text(json.dumps(results,ensure_ascii=False,indent=2))
    print(json.dumps({'action':args.action,'skills':len(results),'verified':True,'report':str(report)},ensure_ascii=False))

if __name__=='__main__':
    main()
