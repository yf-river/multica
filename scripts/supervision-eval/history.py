#!/usr/bin/env python3
"""定位历史纠正候选；候选未经核验，不能直接进入正式题集或作为标准答案。"""
import argparse
import hashlib
import json
from pathlib import Path
import re

PATTERN = re.compile(r'不对|为什么|没(?:有|改|做|实现|完成|理解)|错了|错误|反复|不完整|遗漏|不是|应该|纠正|不需要|不应该')


def candidates(file):
    seen = set()
    session = file.stem.rsplit('rollout-', 1)[-1]
    previous = ''
    count = 0
    for number, line in enumerate(file.open(errors='replace'), 1):
        try:
            event = json.loads(line)
        except ValueError:
            continue
        p = event.get('payload', {})
        if event.get('type') != 'response_item' or p.get('type') != 'message':
            continue
        text = '\n'.join(x.get('text', '') for x in p.get('content', []) if isinstance(x, dict))
        if p.get('role') == 'assistant':
            previous = text
            continue
        if p.get('role') != 'user' or len(text) > 5000 or text.lstrip().startswith(('# AGENTS.md', '# Browser comments:', '<environment_context>', '<send_user_message')):
            continue
        fingerprint = hashlib.sha256(text.encode()).hexdigest()
        if fingerprint in seen or not PATTERN.search(text):
            continue
        seen.add(fingerprint)
        count += 1
        yield dict(id=f'{session}:{number}', source_file=str(file), source_line=number,
                   source_sha256=None, user_text=text, preceding_assistant=previous[:4000],
                   classification='unreviewed_candidate', gold=None)


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--roots', type=Path, nargs='+', required=True)
    p.add_argument('--output', type=Path, required=True)
    args = p.parse_args()
    files = sorted({f for root in args.roots for f in root.rglob('rollout-*.jsonl')})
    args.output.mkdir(parents=True, exist_ok=True)
    args.output.chmod(0o700)
    count = 0
    manifest = []
    source_hashes = set()
    with (args.output/'history-candidates.jsonl').open('w') as output:
        for file in files:
            sha = hashlib.sha256(file.read_bytes()).hexdigest()
            if sha in source_hashes:
                continue
            source_hashes.add(sha)
            items = list(candidates(file))
            if not items:
                continue
            manifest.append(dict(path=str(file), sha256=sha, candidates=len(items)))
            for item in items:
                item['source_sha256'] = sha
                output.write(json.dumps(item, ensure_ascii=False)+'\n')
                count += 1
    (args.output/'history-manifest.json').write_text(json.dumps(dict(files=manifest,candidates=count,
        note='候选来自真实原文，但尚未判断是否为纠正；须核验当时工具证据和后续结果。'),ensure_ascii=False,indent=2)+'\n')
    for saved in args.output.iterdir():
        if saved.is_file():saved.chmod(0o600)
    print(json.dumps(dict(scanned_sessions=len(files), sessions_with_candidates=len(manifest), candidates=count)))


if __name__ == '__main__':
    main()
