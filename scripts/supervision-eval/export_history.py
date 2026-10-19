#!/usr/bin/env python3
"""按人工选定的原文范围导出题目；来源清单与标准答案不放进模型可读目录。"""
import argparse
import hashlib
import hmac
import json
from pathlib import Path
import re
import uuid

from run import write_json


SECRET_KEY = re.compile(r'(?i)(?:password|passwd|secret|token|authorization|api_?key|private_?key|captcha_?(?:answer|code|s))$')
INLINE_SECRET = re.compile(r'''(?ix)(\bBearer\s+)([a-z0-9._~+/=-]{12,})|((?:["']?(?:password|passwd|access_?token|refresh_?token|api_?key|secret_?key|captcha_?(?:answer|code|s))["']?\s*[:=]\s*)["']?)([^\s,"'\\}]{4,})''')
PRIVATE_KEY = re.compile(r'-----BEGIN[^\n]*PRIVATE KEY-----.*?-----END[^\n]*PRIVATE KEY-----', re.S)


def sanitize(value, salt):
    def mask(text):
        return '[private-'+hmac.new(salt,text.encode(),hashlib.sha256).hexdigest()[:16]+']'
    if isinstance(value,dict):
        return {key:mask(str(item)) if SECRET_KEY.search(key) and item else sanitize(item,salt) for key,item in value.items()}
    if isinstance(value,list):
        return [sanitize(item,salt) for item in value]
    if not isinstance(value,str):
        return value
    if value.lstrip().startswith(('{','[')):
        try:
            parsed=json.loads(value)
        except ValueError:
            pass
        else:
            return json.dumps(sanitize(parsed,salt),ensure_ascii=False)
    text=PRIVATE_KEY.sub(lambda m:mask(m[0]),value)
    text=INLINE_SECRET.sub(lambda m:(m[1] or m[3])+mask(m[2] or m[4]),text)
    text=re.sub(r'\b(?:ghp_|glpat-|sk-)[A-Za-z0-9_-]{20,}',lambda m:mask(m[0]),text)
    text=re.sub(r'\bey[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}',lambda m:mask(m[0]),text)
    text=re.sub(r'(?i)((?:postgresql|postgres|mysql|redis)://[^:\s]+:)([^@\s]+)(@)',lambda m:m[1]+mask(m[2])+m[3],text)
    text=re.sub(r'(?i)(\b[^\s:]+:)([^\s@]+)(@tcp\()',lambda m:m[1]+mask(m[2])+m[3],text)
    return text


def visible(payload):
    kind=payload.get('type')
    if kind=='message':
        if payload.get('channel') in ['analysis','summary']:
            return None
        text='\n'.join(c.get('text','') for c in payload.get('content',[]) if isinstance(c,dict))
        if text.lstrip().startswith(('# AGENTS.md','<environment_context>')):
            return None
        return dict(type=kind,role=payload.get('role'),text=text)
    if kind in ['function_call','custom_tool_call']:
        raw=payload.get('arguments',payload.get('input',''))
        try:raw=json.loads(raw)
        except (ValueError,TypeError):pass
        return dict(type=kind,name=payload.get('name'),call_id=payload.get('call_id'),arguments=raw)
    if kind in ['function_call_output','custom_tool_call_output']:
        return dict(type=kind,call_id=payload.get('call_id'),output=payload.get('output',''))
    return None


def export(selection, output, salt):
    cases=[];provenance=[]
    for spec in selection['cases']:
        materials=[];history=[];origins=[]
        for window in spec['windows']:
            source=Path(window['source'])
            raw=source.read_bytes();lines=raw.splitlines(keepends=True)
            first,last=window['first'],window['last']
            if first<1 or last>len(lines) or last<first:
                raise ValueError('来源范围无效：'+spec['id'])
            selected=b''.join(lines[first-1:last])
            if hashlib.sha256(selected).hexdigest()!=window['sha256']:
                raise ValueError('原文范围已变化：'+spec['id'])
            for number in range(first,last+1):
                event=json.loads(lines[number-1])
                if event.get('type')!='response_item':continue
                record=visible(event.get('payload',{}))
                if record is None:continue
                record.update(source_line=number,timestamp=event.get('timestamp'),source_label=window.get('label','历史原文'))
                text=json.dumps(sanitize(record,salt),ensure_ascii=False)
                mid=str(uuid.uuid5(uuid.NAMESPACE_URL,str(source)+':'+str(number)))
                item=dict(kind='trace',id=mid,generation=1,offset=0,end=len(text),length=len(text),text=text)
                history.append(item)
                origins.append(dict(id=mid,source=str(source),line=number,raw_line_sha256=hashlib.sha256(lines[number-1]).hexdigest()))
        # 产品单批材料预算；超出部分只能通过历史工具补查，材料轮须如实保留缺口。
        remaining=48000
        for item in history:
            if len(materials)>=24 or remaining<=0:break
            size=min(8000,len(item['text']),remaining)
            materials.append(dict(item,text=item['text'][:size],end=size))
            remaining-=size
        if not materials:raise ValueError('范围没有可见消息或实际工具记录：'+spec['id'])
        cases.append(dict(id=spec['id'],family=spec['family'],track=spec.get('track','materials'),
                          current=dict(title=spec['title'],description=spec['requirement'],pending_materials=len(history)),
                          materials=materials,history_materials=history))
        provenance.append(dict(id=spec['id'],family=spec['family'],windows=spec['windows'],events=origins,
                               qualification=spec.get('qualification','unreviewed'),gold=spec.get('gold')))
    write_json(output/'cases.json',dict(cases=cases))
    write_json(output/'private-provenance.json',provenance)
    for path in output.iterdir():
        if path.is_file():path.chmod(0o600)
    print(json.dumps(dict(exported=len(cases),qualified=sum(p['qualification']=='qualified' for p in provenance))))


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--selection',type=Path,required=True)
    p.add_argument('--output',type=Path,required=True)
    p.add_argument('--salt-file',type=Path,required=True)
    args=p.parse_args()
    salt=args.salt_file.read_bytes()
    if len(salt)<32:p.error('脱敏密钥至少32字节')
    args.output.mkdir(parents=True,exist_ok=True);args.output.chmod(0o700)
    export(json.loads(args.selection.read_text()),args.output,salt)


if __name__=='__main__':main()
