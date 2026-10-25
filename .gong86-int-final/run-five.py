exec(open('.gong86-int-final/common.py').read())
import subprocess
st,d,c,m,p,cfg=guard()
with (D/'fixed-int-five.log').open('w') as f:
 result=subprocess.run(['go','test','./internal/supervision','-run','^TestInt(ConcurrentDispatchAndLongHistory|PersistenceAndResume|SupplementalReadingAdvancesOnlyContiguousCurrentGeneration|SupervisionStopAllAndOne|SupervisionPreservesNewHumanDecisions)$','-count=1','-v'],cwd=pathlib.Path(c['source'])/'server',env={**os.environ,'MULTICA_SUPERVISION_INT_TEST':'1'},stdout=f,stderr=subprocess.STDOUT)
assert result.returncode==0, 'see fixed-int-five.log'
text=(D/'fixed-int-five.log').read_text();assert text.count('--- PASS: TestInt')==5 and '--- SKIP:' not in text
save('five-summary',dict(passed=5,skipped=0,commit=c['commit'],candidate=c['id'],environment='int'))
print('5 fixed int tests passed')
