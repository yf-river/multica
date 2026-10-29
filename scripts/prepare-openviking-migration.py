#!/usr/bin/env python3
"""从只读盘点生成 Skill 原件包和归属预览；不写数据库或 OpenViking。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re


def rows(root, name):
    with (root / (name + '.jsonl')).open() as source:
        for line in source:
            yield json.loads(line)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    path.chmod(0o600)


def safe_child(root, name):
    rel = Path(name)
    if rel.is_absolute() or '..' in rel.parts or '\\' in name or str(rel) != name:
        raise ValueError('不安全的源路径：' + name)
    candidate = root / rel
    if not candidate.resolve().is_relative_to(root.resolve()):
        raise ValueError('源路径越界：' + name)
    return candidate


def prepare(inventory, destination, ownership):
    destination.mkdir(mode=0o700, parents=True, exist_ok=False)
    manifest = json.loads((inventory / 'inventory.json').read_text())
    upload_root = Path(manifest['roots']['uploads'])
    native_root = Path(manifest['roots']['openviking'])
    skill_files = {}
    for item in rows(inventory, 'skill_file'):
        skill_files.setdefault(item['skill_id'], []).append(item)
    entries, seen = [], set()
    for skill in rows(inventory, 'skill'):
        name = skill['name']
        if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}', name) or name in seen:
            raise ValueError('Skill 名称需要显式消歧：' + name)
        seen.add(name)
        native_name = name.replace('.', '-')
        source = destination / 'skills' / native_name
        source.mkdir(mode=0o700, parents=True)
        data = {'SKILL.md': skill['content'].encode()}
        modes = {}
        for item in skill_files.get(skill['id'], []):
            rel = item['path']
            safe_child(source, rel)
            if item['kind'] == 'binary':
                content = safe_child(upload_root, item['storage_key']).read_bytes()
                if item['sha256'] and digest(content) != item['sha256']:
                    raise ValueError('二进制源哈希不匹配：' + rel)
            else:
                content = item['content'].encode()
            if rel in data and data[rel] != content:
                raise ValueError('Skill 主文档与文件记录冲突：' + name + '/' + rel)
            data[rel] = content
            modes[rel] = item['mode']
        if 'multica-skill.json' in data:
            raise ValueError('Skill 模式旁文件冲突：' + name)
        data['multica-skill.json'] = json.dumps({'modes': modes, 'source_name': name, 'source_skill_id': skill['id'], 'source_workspace_id': skill['workspace_id']}, ensure_ascii=False).encode()
        hashes = {}
        for rel, content in data.items():
            target = safe_child(source, rel)
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            target.write_bytes(content)
            target.chmod(modes.get(rel, 0o644))
            hashes[rel] = digest(content)
        entries.append(dict(kind='skill', source=str(source.resolve()), source_uri='multica://skill/' + skill['id'], user='multica', target='viking://agent/skills/' + native_name, hashes=hashes))
    write(destination / 'skills-plan.json', dict(version=1, entries=entries))

    agents = {a['id']: a for a in rows(inventory, 'agent')}
    categories = {(s['workspace_id'], s['key']): s['category'] for s in rows(inventory, 'issue_status')}
    original = ownership['source_agent']
    updates, preserved, unresolved = [], [], []
    for issue in rows(inventory, 'issue'):
        if issue.get('assignee_type') != 'agent' or issue.get('assignee_id') != original:
            continue
        record = {k: issue.get(k) for k in ['id', 'workspace_id', 'number', 'title', 'project_id', 'status', 'assignee_id', 'revision']}
        category = categories.get((issue['workspace_id'], issue['status']))
        if category in ('done', 'completed', 'cancelled'):
            preserved.append(record)
            continue
        target = ownership['project_users'].get(issue.get('project_id'))
        if target is None or category is None:
            unresolved.append(record)
        else:
            record['target_user'] = target
            updates.append(record)
    write(destination / 'agent-plan.json', dict(source_agent=agents.get(original), users=ownership['project_users'], unfinished=updates, historical_preserved=preserved, unresolved=unresolved, other_owners='保持原负责人，包括 River。只按显式项目映射分配未完成事项。'))

    native = {}
    changed = []
    for item in rows(inventory, 'openviking-files'):
        rel = item['path']
        if item.get('changed_during_read'):
            changed.append(rel)
        parts = Path(rel).parts
        if len(parts) >= 5 and parts[:3] == ('viking', 'default', 'user'):
            user = parts[3]
            record = native.setdefault(user, dict(files=0, sessions=set(), memories=0, skill_files=0))
            record['files'] += 1
            if parts[4] == 'sessions' and len(parts) > 5:
                record['sessions'].add(parts[5])
            if parts[4] == 'memories':
                record['memories'] += 1
            if parts[4] == 'skills':
                record['skill_files'] += 1
    for record in native.values():
        record['sessions'] = sorted(record['sessions'])
    write(destination / 'native-ownership.json', dict(source=str(native_root), users=native, changed_during_read=changed, unresolved='历史用户与记忆归属须由显式映射决定；源目录不删除，不自动合并身份元数据。'))
    return dict(skills=len(entries), source_skill_files=manifest['counts']['skill_file'], unfinished=len(updates), historical_preserved=len(preserved), unresolved=len(unresolved), native_users=len(native), changed_files=len(changed))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--inventory', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--ownership', type=Path, required=True, help='source_agent 和 project_users 的显式映射')
    args = parser.parse_args()
    os.umask(0o077)
    print(json.dumps(prepare(args.inventory, args.out, json.loads(args.ownership.read_text())), ensure_ascii=False))


if __name__ == '__main__':
    main()
