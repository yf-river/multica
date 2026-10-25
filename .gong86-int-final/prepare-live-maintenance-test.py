from pathlib import Path
import subprocess
source=Path('/data/ida/artifacts/quality-token-supervision-20261025/integration-control/live-int-api-retry.py').read_text();commit=subprocess.check_output(['git','-C','/data/ida/worktrees/gong86-integration-only','rev-parse','HEAD'],text=True).strip();source=source.replace('3f2abcb3066ba136168a07f4cdf3c3468a3f14d4',commit).replace('maintenance-e382f385f46827e0a3c60876d2c69c078be65e0d','maintenance-'+commit).replace('integration-control/live-int-api-retry.json','int-recovery-20261025/live-int-api.json')
needle="advance('prepared',candidate_id=candidate,candidate_url=m['frontend_url'])"
addition="""
 advance('fail',error='synthetic deployed int failure')
 call('delivery-queue/publication',dict(lease,action='release'));lease=None
 call('issues/'+issue+'/deliveries/'+delivery+'/action',dict(action='retest',revision=current()['revision'],candidate_id='wrong',evidence=[dict(name='recovery checked',status='passed',evidence='scoped fixture')]),owned=True,expected=409)
 call('issues/'+issue+'/deliveries/'+delivery+'/action',dict(action='retest',revision=current()['revision'],candidate_id=candidate,evidence=[dict(name='recovery checked',status='passed',evidence='scoped fixture')]),owned=True)
 assert current()['phase']=='awaiting_review'
 worker=advance('claim-integration')['worker_token']
 lease=call('delivery-queue/publication',dict(action='claim',delivery_id=delivery,worker_token=worker))
 advance('prepared',candidate_id=candidate,candidate_url=m['frontend_url'])
"""
assert needle in source;source=source.replace(needle,needle+addition).replace("'unauthorized publishing rejected']","'unauthorized publishing rejected','deployed int failure restored through formal retest; wrong candidate rejected']")
Path('.gong86-int-final/live-maintenance-test.py').write_text(source)
