#!/usr/bin/env python3
"""按冻结答案计算榜单；失败调用计入覆盖，重复轮次不冒充独立题目。"""
import argparse
from collections import defaultdict
import json
from pathlib import Path
import statistics
from run import extract, CODES


def generate(root):
    manifest=json.loads((root/'manifest.json').read_text())
    expected={c['key']:c['expected'] for c in manifest['cases']}
    models=defaultdict(list)
    case_rows=[]
    for path in sorted(root.glob('*/r*-g*/status.json')):
        status=json.loads(path.read_text());key=status['runtime']+':'+status['requested_model']
        mapping=json.loads((path.parent/'mapping.json').read_text())
        answers={}
        try:
            if status.get('exit_code') != 0:
                raise ValueError(status.get('error','nonzero exit'))
            answer_file=path.parent/'answer.txt'
            result,usage,actual,credits=extract(status['runtime'],(path.parent/'stdout.jsonl').read_text(),answer_file.read_text() if answer_file.exists() else '',bool(manifest.get('tool_mode')))
            items=result.get('cases',[])
            ids=[x.get('id') for x in items]
            if len(ids)!=len(set(ids)) or set(ids)!=set(mapping):
                raise ValueError('案例缺失、重复或未知ID')
            cases={c['key']:c for c in manifest['cases']}
            for item in items:
                if item.get('verdict') not in ['clear','problem','insufficient','risk']:
                    raise ValueError('invalid verdict')
                valid_ids={x['id'] for x in cases[mapping[item['id']]]['records']}
                for finding in item.get('findings',[]):
                    if finding.get('code') not in CODES or not finding.get('evidence') or not set(finding['evidence'])<=valid_ids:
                        raise ValueError('问题类型或证据引用无效')
            if not status['passed']:
                status['harness_correction']=status.get('error')
            status.update(passed=True)
            status.update(usage=usage,reported_models=actual,credits=credits)
            answers={x['id']:x for x in items}
        except (ValueError,KeyError,TypeError) as error:
            status.update(passed=False,error=str(error))
        models[key].append(status)
        for public_id,case_id in mapping.items():
            answer=answers.get(public_id,{})
            exp=expected[case_id]
            predicted={f['code'] for f in answer.get('findings',[])}
            positive=set(exp['codes']);confirmed=exp['verdict']=='problem'
            evidence={e for f in answer.get('findings',[]) for e in f['evidence']}
            row=dict(model=key,case=case_id,repeat=status['repeat'],valid=status['passed'],
                verdict_correct=answer.get('verdict')==exp['verdict'],expected_verdict=exp['verdict'],predicted_verdict=answer.get('verdict'),
                code_tp=len(positive&predicted),code_fp=len(predicted-positive),code_fn=len(positive-predicted),
                critical=exp['critical'],critical_caught=confirmed and positive<=predicted and answer.get('verdict')=='problem',
                false_alarm=exp['verdict']=='clear' and (bool(predicted) or answer.get('verdict') in ['problem','risk']),
                clear=exp['verdict']=='clear',evidence_coverage=set(exp['citations'])<=evidence,
                missing_codes=sorted(positive-predicted),extra_codes=sorted(predicted-positive))
            case_rows.append(row)
    summary=[]
    for model,calls in models.items():
        rows=[r for r in case_rows if r['model']==model]
        tp=sum(r['code_tp'] for r in rows);fp=sum(r['code_fp'] for r in rows);fn=sum(r['code_fn'] for r in rows)
        precision=tp/(tp+fp) if tp+fp else 1
        recall=tp/(tp+fn) if tp+fn else 1
        critical=[r for r in rows if r['critical']]
        clean=[r for r in rows if r['clear']]
        comparisons=defaultdict(list)
        for r in rows: comparisons[r['case']].append(r['predicted_verdict'])
        stable=[len(set(v))==1 for v in comparisons.values() if len(v)>1]
        credits=[c['credits'] for c in calls if c.get('credits') is not None]
        summary.append(dict(model=model,calls=len(calls),successful_calls=sum(c['passed'] for c in calls),
            independent_cases=len(comparisons),case_evaluations=len(rows),
            verdict_accuracy=sum(r['verdict_correct'] for r in rows)/len(rows),precision=precision,recall=recall,
            f2=5*precision*recall/(4*precision+recall) if precision+recall else 0,
            critical_recall=sum(r['critical_caught'] for r in critical)/len(critical) if critical else None,
            false_alarm_rate=sum(r['false_alarm'] for r in clean)/len(clean) if clean else None,
            repeat_stability=sum(stable)/len(stable) if stable else None,
            harness_corrections=sum('harness_correction' in c for c in calls),
            median_seconds=statistics.median(c['elapsed_seconds'] for c in calls),
            total_credits=sum(credits) if credits else None,
            median_credits=statistics.median(credits) if credits else None))
    summary.sort(key=lambda s:(s['f2'],s['critical_recall'] or 0,-(s['false_alarm_rate'] or 0)),reverse=True)
    report=dict(stage='readonly-tool' if manifest.get('tool_mode') else 'frozen-evidence',complete=all(s['calls']==manifest['groups']*manifest['repeats'] for s in summary) and len(summary)==len(manifest['models']),
        limitations=['构造的监督材料题，不是通用模型榜单','尚不代表实际工具使用和生产监督质量','重复采样用于稳定性，不增加独立案例数','错误类型匹配为客观指标，原因分析还需审阅','积分是该账号本次回执，美元0不表示免费'],models=summary,cases=case_rows)
    (root/'report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
    lines=['# 监督模型材料轮实测榜单','',f'状态：{"完整" if report["complete"] else "进行中，禁止据此决定最终模型"}。',
        '', '| 模型 / 运行时 | 独立题数 | 成功调用 | F2 | 关键问题召回 | 无问题误报率 | 结论准确率 | 中位耗时s | 中位积分/组 |',
        '|---|---:|---:|---:|---:|---:|---:|---:|---:|']
    def pct(x):return '—' if x is None else f'{x*100:.1f}%'
    for s in summary:
        lines.append(f'| {s["model"]} | {s["independent_cases"]} | {s["successful_calls"]}/{s["calls"]} | {pct(s["f2"])} | {pct(s["critical_recall"])} | {pct(s["false_alarm_rate"])} | {pct(s["verdict_accuracy"])} | {s["median_seconds"]:.1f} | {s["median_credits"] if s["median_credits"] is not None else "未返回"} |')
    lines+=['','F2 优先反映召回，同时惩罚额外问题类型；须结合误报和证据审阅，不作为单一准入门槛。',
        '','## 证据边界','']+['- '+s for s in report['limitations']]
    (root/'report.md').write_text('\n'.join(lines)+'\n')
    print('\n'.join(lines))


if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('root',type=Path);generate(p.parse_args().root)
