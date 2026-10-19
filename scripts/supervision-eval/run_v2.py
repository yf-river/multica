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
import uuid
import threading

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
        target = cwd/('source-'+hashlib.sha256(source['id'].encode()).hexdigest()[:16])
        if not target.exists():
            shutil.copytree(original, target, symlinks=True)
        roots.append(dict(id=source['id'], path=str(target), run_id=source.get('run_id', ''),label=source.get('label','关联材料')))
    bundle = dict(batch=case['id'], current=json.dumps(case['current'], ensure_ascii=False),
                  summary=case.get('previous_summary', ''), open=json.dumps(case.get('open_findings', []), ensure_ascii=False),
                  materials=case['materials'], investigation=case['track']=='tools')
    write_json(dest/'input.json', bundle)
    prompt = subprocess.check_output([str(args.binary), 'supervision-prompt', str(dest/'input.json')], text=True)
    audit = dest/'audit.jsonl'
    if case['track']=='tools':
        prompt += '\n授权来源：'+json.dumps([{k:v for k,v in r.items() if k!='path'} for r in roots],ensure_ascii=False)
        prompt += '\n目录内容是调查时快照，指定历史提交才代表历史源码。模型只能使用专用调查工具。'
    (dest/'prompt.txt').write_text(prompt)
    cmd = ['codebuddy', '-p', '--model', model, '--effort', 'high', '--tools', '',
           '--strict-mcp-config', '--setting-sources', '', '--no-session-persistence',
           '--output-format', 'json', '--max-turns', '40' if case['track']=='tools' else '1']
    if case['track']=='tools':
        write_json(dest/'scope.json', dict(roots=roots, materials=case.get('history_materials',case['materials']), task_id='', local_audit=str(audit)))
        write_json(dest/'mcp.json',dict(mcpServers=dict(supervision=dict(command=str(args.binary),args=['supervision-mcp',str(dest/'scope.json')]))))
        cmd += ['--mcp-config',str(dest/'mcp.json'),'--permission-mode','bypassPermissions']
    env = os.environ.copy()
    for key in ['MULTICA_TOKEN','MULTICA_TASK_ID','MULTICA_AGENT_ID','MULTICA_DAEMON_PORT']:
        env.pop(key,None)
    env.update(CODEBUDDY_CONFIG_DIR=args.codebuddy_config, PWD=str(cwd),
               CODEBUDDY_DEFER_TOOL_LOADING='false',CODEBUDDY_DISABLE_AUTO_MEMORY='1',CODEBUDDY_CODE_DISABLE_AUTO_MEMORY='1')
    start = time.monotonic()
    baseline={r['id']:tree_digest(Path(r['path'])) for r in roots}
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
        (dest/'answer-text.txt').write_text(answer_text)
        captures = [json.loads(l)['result'] for l in audit.read_text().splitlines()] if audit.exists() else []
        refs = [dict(kind='investigation',id=c['id'],run_id=c.get('run_id','')) for c in captures]
        validation = dict(output=answer_text,materials=case['materials'],
                          investigations=[dict(**r,generation=1,offset=0,end=0,length=0) for r in refs],
                          known_findings=case.get('known_findings',[]))
        write_json(dest/'validation.json',validation)
        check = subprocess.run([str(args.binary),'supervision-validate',str(dest/'validation.json')],capture_output=True,text=True,timeout=15)
        (dest/'validation.log').write_text(check.stdout+check.stderr)
        if check.returncode==0:
            write_json(dest/'answer.json',json.loads(check.stdout))
        else:
            # 语义复核可查看失败回答，但不能把提取到的JSON冒充线上可保存的结果。
            try:
                write_json(dest/'unvalidated-answer.json',parse_json(answer_text))
            except ValueError:
                pass
        status.update(valid=check.returncode==0,usage=usage,reported_models=actual,credits=credits,
                      investigation_reads=len(captures),successful_reads=sum(not c.get('error') for c in captures),
                      error=None if check.returncode==0 else (check.stdout+check.stderr)[:1000])
    except Exception as error:
        status.update(valid=False,error=str(error))
    if (dest/'stdout.jsonl').exists():
        try:
            events=json.loads((dest/'stdout.jsonl').read_text())
            events=events if isinstance(events,list) else [events]
            calls=[e for e in events if e.get('type')=='function_call']
            write_json(dest/'tool-calls.json',calls)
            status['tool_calls']=len(calls)
            status['tool_names']=sorted({e.get('name','') for e in calls})
            allowed={'mcp__supervision__supervision_'+suffix for suffix in ['read','search','git','material']}
            status['unexpected_tool_attempts']=[e.get('name','') for e in calls if e.get('name') not in allowed]
        except ValueError:
            status['tool_trace_unreadable']=True
    status['sources_unchanged']=all(tree_digest(Path(r['path']))==baseline[r['id']] for r in roots)
    if not status['sources_unchanged']:
        status.update(valid=False,error='只读来源发生变化，需检查实际写入路径')
    status['elapsed_seconds']=round(time.monotonic()-start,3)
    write_json(status_file,status)
    print(json.dumps({k:status.get(k) for k in ['model','case','valid','elapsed_seconds','investigation_reads','error']},ensure_ascii=False),flush=True)
    return status


def merge_pending(previous, incoming):
    """失败批次不消费游标；同一来源的新版本替代旧版本，分片从最早未读位置重送。"""
    pending = {(m['kind'],m['id']):dict(m) for m in previous}
    for material in incoming:
        key=(material['kind'],material['id'])
        old=pending.get(key)
        if old is None or material['generation']>old['generation']:
            pending[key]=dict(material)
        elif material['generation']==old['generation'] and material['offset']<old['offset']:
            pending[key]=dict(material)
    return list(pending.values())


def advance_ledger(ledger, answer, namespace):
    """宿主分配问题ID，不替模型修复标签、证据或问题复用决定。"""
    updated={key:dict(value) for key,value in ledger.items()}
    for finding in answer['findings']:
        row=dict(finding)
        if not row.get('id'):
            evidence=row['evidence'][0]
            fingerprint=row['code']+':'+evidence['kind']+':'+evidence['id']
            row['id']=str(uuid.uuid5(uuid.NAMESPACE_URL,namespace+':'+fingerprint))
        row['status']='open'
        updated[row['id']]=row
    for resolution in answer['resolutions']:
        updated[resolution['id']]['status']='resolved'
    return updated


def select_batch(pending):
    batch=[]
    remaining=48000
    for material in pending:
        if len(batch)>=24 or remaining<=0:
            break
        size=min(8000,len(material.get('text','')),remaining)
        batch.append(dict(material,text=material.get('text','')[:size],end=material['offset']+size))
        remaining-=size
    return batch


def advance_pending(pending, delivered, history):
    """覆盖一页不等于读完原文；与产品逐段模式一样保留尚未消费的尾部。"""
    remaining={(m['kind'],m['id']):dict(m) for m in pending}
    for material in delivered:
        key=(material['kind'],material['id'])
        if material['end']>=material['length']:
            remaining.pop(key,None)
            continue
        source=history.get(key)
        if source is None or source['generation']!=material['generation'] or source['offset']!=0 or len(source.get('text',''))<source['length']:
            raise ValueError('冻结材料缺少未读原文，不能推进回放：'+material['id'])
        start=material['end'];end=min(start+8000,source['length'])
        remaining[key]=dict(source,offset=start,end=end,text=source['text'][start:end])
    return list(remaining.values())


def run_story(model, story, repeat, args):
    ledger={f['id']:dict(f) for f in story.get('initial_findings',[])}
    pending=[]
    summary=story.get('previous_summary','')
    previous_error=''
    history={}
    for stage in story['stages']:
        case=dict(stage,id=story['id']+'/'+stage['id'],family=story['family'])
        deleted={(m['kind'],m['id']) for m in stage.get('deleted_materials',[])}
        pending=[m for m in pending if (m['kind'],m['id']) not in deleted]
        for key in deleted:
            history.pop(key,None)
        pending=merge_pending(pending,stage['materials'])
        case['materials']=select_batch(pending)
        case['current']=dict(stage['current'],pending_materials=len(pending))
        if previous_error:
            case['current']['previous_attempt_error']=previous_error
        case['previous_summary']=stage.get('summary_override',summary)
        case['open_findings']=[f for f in ledger.values() if f.get('status','open')!='resolved']
        case['known_findings']=list(ledger)
        for item in stage.get('history_materials',stage['materials']):
            history[(item['kind'],item['id'])]=item
        case['history_materials']=list(history.values())
        result=run_case(model,case,repeat,args)
        dest=args.output/model/f'r{repeat}'/case['id']
        if result['valid']:
            answer=json.loads((dest/'answer.json').read_text())
            ledger=advance_ledger(ledger,answer,story['id'])
            summary=answer['summary']
            pending=advance_pending(pending,case['materials'],history)
            previous_error=''
        else:
            previous_error=result.get('error','')
        audit=dest/'audit.jsonl'
        if audit.exists():
            for line in audit.read_text().splitlines():
                record=json.loads(line)
                capture=record['result']
                text=json.dumps(record,ensure_ascii=False)
                material=dict(kind='investigation',id=capture['id'],run_id=capture.get('run_id',''),
                              generation=1,offset=0,end=len(text),length=len(text),text=text)
                history[('investigation',capture['id'])]=material
        # 记录真实状态，失败结果不能悄悄成为下一轮的已确认结论。
        write_json(dest/'state-after.json',dict(summary=summary,findings=list(ledger.values()),pending=pending))
    return result


def tree_digest(root):
    digest=hashlib.sha256()
    for path in sorted(root.rglob('*')):
        if path.is_symlink():
            value=('symlink:'+os.readlink(path)).encode()
        elif path.is_file():
            value=path.read_bytes()
        else:
            continue
        digest.update(str(path.relative_to(root)).encode()+b'\0'+hashlib.sha256(value).digest())
    return digest.hexdigest()


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
    data=json.loads(args.dataset.read_text());cases=data.get('cases',[]);stories=data.get('stories',[])
    ids=[c['id'] for c in cases]+[s['id'] for s in stories]
    if len(ids)!=len(set(ids)):
        p.error('duplicate case IDs')
    if any('/' in key or key in ['.','..'] for key in ids):
        p.error('case IDs must be single path components')
    for story in stories:
        stages=[s['id'] for s in story['stages']]
        if len(stages)!=len(set(stages)) or any('/' in key or key in ['.','..'] for key in stages):
            p.error('invalid story stage IDs')
    roots={str((args.dataset.parent/r['path']).resolve()) for c in cases+[s for story in stories for s in story['stages']] for r in c.get('roots',[])}
    frozen=dict(dataset_sha256=hashlib.sha256(args.dataset.read_bytes()).hexdigest(),
                binary_sha256=hashlib.sha256(args.binary.read_bytes()).hexdigest(),models=args.models,repeats=args.repeats,
                runner_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                parser_sha256=hashlib.sha256(Path(__file__).with_name('run.py').read_bytes()).hexdigest(),
                roots={root:tree_digest(Path(root)) for root in sorted(roots)},
                cli_version=subprocess.check_output(['codebuddy','--version'],text=True).strip(),
                workers=args.workers,per_model_concurrency=max(1,args.workers//len(args.models)),
                effort='high',timeout_seconds=600,max_tool_turns=40,max_material_turns=1,
                deferred_tools=False,auto_memory=False,setting_sources=[])
    args.output.mkdir(parents=True,exist_ok=True)
    manifest=args.output/'manifest.json'
    if manifest.exists() and json.loads(manifest.read_text())!=frozen:
        p.error('输入、工具二进制或配置已变化，必须使用新的输出目录')
    write_json(manifest,frozen)
    shutil.copyfile(Path(__file__),args.output/'runner-frozen.py')
    shutil.copyfile(Path(__file__).with_name('run.py'),args.output/'parser-frozen.py')
    jobs=[(m,c,r) for r in range(args.repeats) for c in cases for m in args.models]
    jobs += [(m,s,r) for r in range(args.repeats) for s in stories for m in args.models]
    limits={model:threading.Semaphore(frozen['per_model_concurrency']) for model in args.models}
    def execute(job):
        with limits[job[0]]:
            return (run_story if 'stages' in job[1] else run_case)(*job,args)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        list(pool.map(execute,jobs))


if __name__=='__main__':
    main()
