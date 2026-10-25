import importlib.util,json
s=importlib.util.spec_from_file_location('a','scripts/prepare-supervision-acceptance.py');a=importlib.util.module_from_spec(s);s.loader.exec_module(a)
m=json.load(open('/data/ida/multica/.run/deployments/goal-test-verify-1.json'));p=json.load(open(m['daemon_profile_path']))
def api(method,path,body=None):return a.api(m,p,method,path,body)
