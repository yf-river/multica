exec(open('.gong86-verify87/setup.py').read().split('agent=')[0])
for n in ['codebuddy','codex','waste','child','cancel']:
 i=json.load(open(D/(n+'-issue.json')));s=api('GET','supervision/subjects/'+i['id']);save(n+'-subject',s)
 for t in s['trace_materials']:
  pages=[];offset=0
  while True:
   page=api('GET','supervision/evidence/trace_usage/'+t['id']+'?offset='+str(offset));assert page['offset']==offset and not page['deleted'];pages.append(page)
   if page['end']>=page['length']:break
   assert page['end']>offset;offset=page['end']
  assert len({(x['generation'],x['sha256']) for x in pages})==1
  save(n+'-pages-'+t['id'],pages);report=json.loads(''.join(x['text'] for x in pages));save(n+'-report-'+t['id'],report)
  print(n,t['id'],len(pages),report['preparation_status'],'comparisons',len(report.get('request_comparisons') or []),'gaps',report.get('gaps'))
 print('batches',n,[(x['status'],x['task_id'],x.get('error')) for x in s['batches']])
