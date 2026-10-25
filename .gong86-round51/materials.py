import sys
sys.path.insert(0,'.gong86-current');from round51 import *
for n in ['codebuddy','codex','child','cancel','waste']:
 if not (D/(n+'-issue.json')).exists():continue
 s=api('GET','supervision/subjects/'+json.load(open(D/(n+'-issue.json')))['id']);save(n+'-subject',s)
 for t in s['trace_materials']:
  if t['status'] in ['pending','processing']:continue
  pages=[];offset=0
  while True:
   page=api('GET','supervision/evidence/trace_usage/'+t['id']+'?offset='+str(offset));assert page['offset']==offset and not page['deleted'];pages.append(page)
   if page['end']>=page['length']:break
   assert page['end']>offset;offset=page['end']
  assert len({(x['generation'],x['sha256']) for x in pages})==1
  save(n+'-pages-'+t['id'],pages);report=json.loads(''.join(x['text'] for x in pages));save(n+'-report-'+t['id'],report)
  print(n,t['status'],t['id'],'pages',len(pages),'matched',t['matched_requests'],'http',t['preparation_http_requests'],'gaps',report.get('gaps'))
 print('batches',n,[(b['status'],b.get('error')) for b in s['batches']])
