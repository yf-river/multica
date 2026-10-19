#!/usr/bin/env python3
"""五模型产品契约评测；模型只看输入与授权来源，答案留在启动器之外。"""
import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time

from run import extract, write_json, parse_json

MODELS = ['gpt-6.1-sol', 'hy4-preview-ioa', 'glm-5.3-ioa', 'deepseek-v4.1-flash', 'kimi-k3-ioa']


def run_case(model, case, repeat, args):
    dest = args.output/model/f"r{repeat}"/case['id']
    dest.mkdir(parents=True, exist_ok=True)
    status_file = dest/'status.json'
    if status_file.exists():
        return json.loads(status_file.read_text())
    cwd = dest/'cwd'
    cwd.mkdir(exist_ok=True)
    roots = []
    for source in case.get('roots', []):
        original = (args.dataset.parent/source['path']).resolve()
        target = cwd/source['id']
        if not target.exists():
            shutil.copytree(original, target, symlinks=True)
        roots.append(dict(id=source['id'], path=str(target), run_id=source.get('run_id', '')))
    bundle = dict(batch=case['id'], current=json.dumps(case['current'], ensure_ascii=False),
                  summary=case.get('previous_summary', ''), open=json.dumps(case.get('open_findings', []), ensure_ascii=False),
                  materials=case['materials'], investigation=case['track']=='tools')
    write_json(dest/'input.json', bundle)
    prompt = subprocess.check_output([str(args.binary), 'supervision-prompt', str(dest/'input.json')], text=True)
    audit = dest/'audit.jsonl'
    if roots:
        prompt += '\n授权来源：'+json.dumps([{k:v for k,v in r.items() if k!='path'} for r in roots],ensure_ascii=False)
        prompt += '\n目录内容是调查时快照，指定历史提交才代表历史源码。模型只能使用专用调查工具。'
    (dest/'prompt.txt').write_text(prompt)
    cmd = ['codebuddy', '-p', '--model', model, '--effort', 'high', '--tools', '',
           '--strict-mcp-config', '--setting-sources', '', '--no-session-persistence',
           '--output-format', 'json', '--max-turns', '40' if roots else '1']
    if roots:
        write_json(dest/'scope.json', dict(roots=roots, task_id='', local_audit=str(audit)))
        write_json(dest/'mcp.json',dict(mcpServers=dict(supervision=dict(command=str(args.binary),args=['supervision-mcp',str(dest/'scope.json')]))))
        cmd += ['--mcp-config',str(dest/'mcp.json'),'--permission-mode','bypassPermissions']
    env = os.environ.copy()
    for key in ['MULTICA_TOKEN','MULTICA_TASK_ID','MULTICA_AGENT_ID','MULTICA_DAEMON_PORT']:
        env.pop(key,None)
    env.update(CODEBUDDY_CONFIG_DIR=args.codebuddy_config, PWD=str(cwd),
               CODEBUDDY_DEFER_TOOL_LOADING='false',CODEBUDDY_DISABLE_AUTO_MEMORY='1',CODEBUDDY_CODE_DISABLE_AUTO_MEMORY='1')
    start = time.monotonic()
    status = dict(model=model, case=case['id'], repeat=repeat, track=case['track'],
                  case_family=case['family'], prompt_sha256=hashlib.sha256(prompt.encode()).hexdigest())
    try:
        with (dest/'stdout.jsonl').open('w') as out, (dest/'stderr.log').open('w') as err:
            process = subprocess.Popen(cmd,stdin=subprocess.PIPE,stdout=out,stderr=err,cwd=cwd,env=env,text=True,start_new_session=True)
            try:
                process.communicate(prompt,timeout=600)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid,signal.SIGTERM)
                try: process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid,signal.SIGKILL);process.wait()
                raise ValueError('runtime timeout')
        if process.returncode:
            raise ValueError('CLI exit '+str(process.returncode))
        raw = (dest/'stdout.jsonl').read_text()
        answer_text,usage,actual,credits = extract('codebuddy',raw,'',True,parse_result=False)
        status.update(usage=usage,reported_models=actual,credits=credits)
        answer=parse_json(answer_text)
        captures = [json.loads(l)['result'] for l in audit.read_text().splitlines()] if audit.exists() else []
        refs = [dict(kind='investigation',id=c['id'],run_id=c.get('run_id','')) for c in captures]
        validation = dict(output=json.dumps(answer,ensure_ascii=False),materials=case['materials'],
                          investigations=[dict(**r,generation=1,offset=0,end=0,length=0) for r in refs])
        write_json(dest/'validation.json',validation)
        check = subprocess.run([str(args.binary),'supervision-validate',str(dest/'validation.json')],capture_output=True,text=True,timeout=15)
        (dest/'validation.log').write_text(check.stdout+check.stderr)
        write_json(dest/'answer.json',answer)
        status.update(valid=check.returncode==0,usage=usage,reported_models=actual,credits=credits,
                      investigation_reads=len(captures),error=None if check.returncode==0 else (check.stdout+check.stderr)[:1000])
    except Exception as error:
        status.update(valid=False,error=str(error))
    status['elapsed_seconds']=round(time.monotonic()-start,3)
    write_json(status_file,status)
    print(json.dumps({k:status.get(k) for k in ['model','case','valid','elapsed_seconds','investigation_reads','error']},ensure_ascii=False),flush=True)
    return status


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--dataset',type=Path,required=True)
    p.add_argument('--output',type=Path,required=True)
    p.add_argument('--binary',type=Path,required=True)
    p.add_argument('--codebuddy-config',required=True)
    p.add_argument('--models',nargs='+',choices=MODELS,default=MODELS)
    p.add_argument('--repeats',type=int,default=3)
    p.add_argument('--workers',type=int,default=3)
    args=p.parse_args()
    args.dataset=args.dataset.resolve();args.output=args.output.resolve();args.binary=args.binary.resolve()
    data=json.loads(args.dataset.read_text());cases=data['cases']
    ids=[c['id'] for c in cases]
    if len(ids)!=len(set(ids)):
        p.error('duplicate case IDs')
    frozen=dict(dataset_sha256=hashlib.sha256(args.dataset.read_bytes()).hexdigest(),
                binary_sha256=hashlib.sha256(args.binary.read_bytes()).hexdigest(),models=args.models,repeats=args.repeats)
    args.output.mkdir(parents=True,exist_ok=True)
    manifest=args.output/'manifest.json'
    if manifest.exists() and json.loads(manifest.read_text())!=frozen:
        p.error('输入、工具二进制或配置已变化，必须使用新的输出目录')
    write_json(manifest,frozen)
    jobs=[(m,c,r) for r in range(args.repeats) for c in cases for m in args.models]
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        list(pool.map(lambda j:run_case(*j,args),jobs))


if __name__=='__main__':
    main()
