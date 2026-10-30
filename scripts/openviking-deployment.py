#!/usr/bin/env python3
"""OpenViking 固定版本候选、隔离验收与本机服务切换；目标配置仅由运维提供。"""
import argparse, base64, contextlib, fcntl, hashlib, json, os, re, shutil, signal, socket, subprocess, sys, time, urllib.request
from pathlib import Path


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    path = Path(path); path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix('.tmp')
    with tmp.open('w') as stream:
        os.fchmod(stream.fileno(),0o600);stream.write(json.dumps(value,ensure_ascii=False,indent=2)+'\n');stream.flush();os.fsync(stream.fileno())
    os.replace(tmp,path)
    descriptor=os.open(path.parent,os.O_RDONLY)
    try:os.fsync(descriptor)
    finally:os.close(descriptor)


def run(args, **kwargs):
    return subprocess.run([str(x) for x in args], check=True, **kwargs)


def get(url, timeout=10):
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(url, timeout=timeout) as response:
        return json.load(response)


def config(path):
    value = read(path)
    required = ('environment','root','config_file','service')
    if any(not value.get(k) for k in required): raise RuntimeError('部署目标配置不完整')
    root = Path(value['root']).resolve()
    production = Path('/data/ida/openviking').resolve()
    if value['environment'] != 'prod' and (root == production or production in root.parents or Path(value['config_file']).resolve() == Path('/root/.openviking/ov.conf')):
        raise RuntimeError('测试部署目标不能使用生产目录或配置')
    if not root.is_absolute() or not re.fullmatch(r'[a-zA-Z0-9_.@-]+\.service',value['service']): raise RuntimeError('部署目标无效')
    return value


def installed(cfg):
    state = Path(cfg['root'])/'deployments/current.json'
    if state.exists(): return read(state)['version']
    python = Path(cfg['root'])/'venv/bin/python'
    return subprocess.check_output([str(python),'-c','from importlib.metadata import version;print(version("openviking"))'],text=True).strip()


def source_info(version=None):
    info = get('https://pypi.org/pypi/openviking/'+(version+'/' if version else '')+'json')
    return {'version':info['info']['version'],'files':[{'filename':f['filename'],'sha256':f['digests']['sha256']} for f in info['urls']]}


def wait_health(url, process=None):
    for _ in range(120):
        if process is not None and process.poll() is not None: raise RuntimeError('候选服务启动失败，请查看 candidate.log')
        try:
            health=get(url+'/health',2)
            if health.get('healthy') is True:return health
            raise RuntimeError('服务健康状态未通过')
        except Exception: time.sleep(.5)
    raise RuntimeError('OpenViking 未在规定时间内恢复健康')


def candidate_dir(cfg, version):
    if not re.fullmatch(r'[0-9]+(?:\.[0-9]+){1,3}',version): raise RuntimeError('仅支持明确的正式版本号')
    return Path(cfg['root'])/'deployments/versions'/version


def deployment_environment(cfg):
    import shlex
    env=os.environ.copy()
    path=cfg.get('env_file')
    if path:
        for line in Path(path).read_text().splitlines():
            if not line.strip() or line.lstrip().startswith('#'):continue
            values=shlex.split(line,comments=True)
            if len(values)!=1 or '=' not in values[0]:raise RuntimeError('部署环境文件格式无效')
            key,value=values[0].split('=',1);env[key]=value
    return env


@contextlib.contextmanager
def instance(program,conf,storage,cfg):
    env=deployment_environment(cfg);env['OPENVIKING_CONFIG_FILE']=str(conf)
    log=(storage/'compatibility.log').open('a')
    process=subprocess.Popen([str(program),'--config',str(conf)],cwd=storage,env=env,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
    url='http://127.0.0.1:'+str(read(conf)['server']['port'])
    try:
        wait_health(url,process);yield url
    finally:
        if process.poll() is None:
            os.killpg(process.pid,signal.SIGTERM)
            try:process.wait(timeout=20)
            except subprocess.TimeoutExpired:os.killpg(process.pid,signal.SIGKILL);process.wait()
        log.close()


def native_request(url,path,body=None):
    data=json.dumps(body).encode() if body is not None else None
    req=urllib.request.Request(url+path,data=data,headers={'Content-Type':'application/json','X-OpenViking-Account':'default','X-OpenViking-User':'multica'})
    try:
        with urllib.request.urlopen(req,timeout=60) as response:
            value=json.load(response)
            if value.get('status')!='ok':raise RuntimeError('原生 API 未成功: '+path)
            return value.get('result')
    except urllib.error.HTTPError as error:
        if error.code==409 and path=='/api/v1/fs/mkdir':return {}
        raise



def check_session_flow(url,session,needle):
    native_request(url,'/api/v1/sessions',{'session_id':session,'auto_commit_policy':None})
    native_request(url,'/api/v1/sessions/'+session+'/messages/batch',{'messages':[{'role':'user','parts':[{'type':'text','text':'请记住本次部署验收的项目代号 '+needle+'，回答偏好是先给简短结论。'}],'source_message_ids':['user-'+session]},{'role':'assistant','parts':[{'type':'text','text':'已记住项目代号 '+needle+'，回答先给简短结论。'}],'source_message_ids':['answer-'+session]}]})
    commit=native_request(url,'/api/v1/sessions/'+session+'/commit',{});task=commit.get('task_id') if isinstance(commit,dict) else None
    if not task:raise RuntimeError('会话提交未返回提取任务')
    deadline=time.monotonic()+180
    while time.monotonic()<deadline:
        status=native_request(url,'/api/v1/tasks/'+task)
        if status.get('status') in ('completed','success'):break
        if status.get('status') in ('failed','error'):raise RuntimeError('部署后的会话提取失败')
        time.sleep(2)
    else:raise RuntimeError('部署后的会话提取超时')
    found=native_request(url,'/api/v1/search/find',{'query':needle,'target_uri':'viking://user/multica/memories','limit':10,'read_content':True})
    if needle not in json.dumps(found,ensure_ascii=False):raise RuntimeError('部署后的跨会话召回未找到本次验收原件')
    return {'session':session,'extraction_task':task,'recall_verified':True,'needle':needle}


def compatibility(cfg,target,conf,storage):
    old=Path(cfg['root'])/'venv/bin/openviking-server'
    state=Path(cfg['root'])/'deployments/current.json'
    if state.exists():old=Path(read(state)['candidate'])/'venv/bin/openviking-server'
    new=target/'venv/bin/openviking-server'
    root='viking://resources/multica-deployment-acceptance'
    nonce=str(time.time_ns());resource=root+'/'+nonce;session='deployment-'+nonce
    payload=b'MULTICA-COMPATIBILITY-'+nonce.encode()+b'\n'+b'x'*300000
    def save(url,name,data):
        native_request(url,'/api/v1/content/batch-write',{'root_uri':resource,'operations':[{'uri':resource+'/'+name,'content_base64':base64.b64encode(data).decode(),'mode':'create'}],'defer_processing':True,'skip_conflicts':True,'wait':True})
    def download(url,name):
        import urllib.parse
        req=urllib.request.Request(url+'/api/v1/content/download?'+urllib.parse.urlencode({'uri':resource+'/'+name}),headers={'X-OpenViking-Account':'default','X-OpenViking-User':'multica'})
        with urllib.request.urlopen(req,timeout=30) as response:return response.read()
    with instance(old,conf,storage,cfg) as url:
        native_request(url,'/api/v1/fs/mkdir',{'uri':root})
        native_request(url,'/api/v1/fs/mkdir',{'uri':resource})
        save(url,'old.txt',payload)
        native_request(url,'/api/v1/sessions',{'session_id':session,'auto_commit_policy':None})
        native_request(url,'/api/v1/sessions/'+session+'/messages/batch',{'messages':[{'role':'user','parts':[{'type':'text','text':'部署验收项目代号 AmberBridge，记住此偏好：先给简短结论。'}],'source_message_ids':['deploy-'+nonce]}]})
    with instance(new,conf,storage,cfg) as url:
        if download(url,'old.txt')!=payload:raise RuntimeError('候选无法读取旧版原件')
        native_request(url,'/api/v1/sessions/'+session)
        save(url,'new.txt',payload[::-1])
        native_request(url,'/api/v1/sessions/'+session+'/messages/batch',{'messages':[{'role':'assistant','parts':[{'type':'text','text':'项目代号 AmberBridge，回答先给简短结论。'}],'source_message_ids':['answer-'+nonce]}]})
        commit=native_request(url,'/api/v1/sessions/'+session+'/commit',{})
        task=commit.get('task_id') if isinstance(commit,dict) else None
        if not task:raise RuntimeError('会话提交未返回提取任务')
        deadline=time.monotonic()+180
        while time.monotonic()<deadline:
            status=native_request(url,'/api/v1/tasks/'+task)
            if status.get('status') in ('completed','success'):break
            if status.get('status') in ('failed','error'):raise RuntimeError('候选会话提取失败')
            time.sleep(2)
        else:raise RuntimeError('候选会话提取未在规定时间内完成')
        native_request(url,'/api/v1/search/find',{'query':'AmberBridge','target_uri':'viking://user/multica/memories','limit':5,'read_content':True})
    with instance(new,conf,storage,cfg) as url:
        recall=check_session_flow(url,'recall-'+nonce,'AmberBridge'+nonce)
    with instance(old,conf,storage,cfg) as url:
        if download(url,'old.txt')!=payload or download(url,'new.txt')!=payload[::-1]:raise RuntimeError('旧版无法读取候选写入的数据')
        native_request(url,'/api/v1/sessions/'+session)
    return {'old_to_new_read':True,'new_to_old_read':True,'long_content_sha256':hashlib.sha256(payload).hexdigest(),'session_commit':task,'recall':recall,'data_directory':str(storage/'data')}


def prepare(cfg, version):
    current=Path(cfg['root'])/'deployments/current.json'
    if current.exists() and read(current).get('status')=='switching':raise RuntimeError('上次部署尚未完成，请先恢复旧版本')
    target = candidate_dir(cfg,version); target.mkdir(parents=True,exist_ok=True)
    origin = source_info(version)
    original_config = read(cfg['config_file'])
    digest = hashlib.sha256(Path(cfg['config_file']).read_bytes()).hexdigest()
    manifest = target/'candidate.json'
    if manifest.exists():
        previous = read(manifest)
        if previous.get('status') == 'verified' and previous.get('config_sha256') == digest and previous.get('base_version')==installed(cfg) and previous.get('verification_version')==2: return previous
    write(manifest,{'status':'preparing','version':version,'source':origin,'config_sha256':digest})
    python = target/'venv/bin/python'
    state = Path(cfg['root'])/'deployments/current.json'
    active = state.exists() and Path(read(state)['candidate']).resolve()==target.resolve()
    # 当前服务的程序只读复用；重新验证配置时也不向运行中的环境安装依赖。
    if not active:
        with (target/'build.log').open('a') as log:
            if not python.exists(): run([sys.executable,'-m','venv',target/'venv'],stdout=log,stderr=log)
            uv = shutil.which('uv')
            if uv: run([uv,'pip','install','--python',python,'openviking=='+version],stdout=log,stderr=log)
            else: run([python,'-m','pip','install','openviking=='+version],stdout=log,stderr=log)
    storage = target/'verification'; storage.mkdir(exist_ok=True)
    isolated = json.loads(json.dumps(original_config))
    isolated['storage']['workspace'] = str(storage/'data')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1',0)); port = sock.getsockname()[1]
    isolated.setdefault('server',{}).update(host='127.0.0.1',port=port)
    isolated.setdefault('storage',{}).setdefault('agfs',{})['backend']='local'
    isolated['storage'].setdefault('vectordb',{})['backend']='local'
    conf = storage/'ov.conf'; write(conf,isolated)
    env = deployment_environment(cfg); env['OPENVIKING_CONFIG_FILE']=str(conf)
    log = (storage/'candidate.log').open('a')
    process = subprocess.Popen([str(target/'venv/bin/openviking-server'),'--config',str(conf)],cwd=storage,env=env,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
    try:
        url = f'http://127.0.0.1:{port}'; health = wait_health(url,process)
        # 原生版本和核心路由必须都能响应；业务验收由 Multica 的既有专项套件继续执行。
        paths = {}
        for path in ['/api/v1/system/status','/openapi.json']:
            try: paths[path] = get(url+path)
            except Exception as error: raise RuntimeError('候选核心接口失败: '+path) from error
        actual = subprocess.check_output([str(python),'-c','from importlib.metadata import version;print(version("openviking"))'],text=True).strip()
        if actual != version: raise RuntimeError('候选实际安装版本不一致')
        result = {'status':'prepared','version':version,'source':origin,'config_sha256':digest,'isolated_url':url,'health':health,'routes':list(paths),'verified_at':time.time(),'program':str(target/'venv/bin/openviking-server')}
        write(manifest,result)
    finally:
        if process.poll() is None:
            os.killpg(process.pid,signal.SIGTERM)
            try: process.wait(timeout=20)
            except subprocess.TimeoutExpired: os.killpg(process.pid,signal.SIGKILL);process.wait()
        log.close()
    evidence=compatibility(cfg,target,conf,storage)
    result.update(status='verified',verification_version=2,data_compatible=True,rollback_compatible=True,base_version=installed(cfg),compatibility=evidence)
    write(manifest,result)
    return result


def deploy(cfg,version):
    target = candidate_dir(cfg,version)
    current_file=Path(cfg['root'])/'deployments/current.json'
    if current_file.exists() and read(current_file).get('status')=='switching':raise RuntimeError('上次部署尚未完成，请先恢复旧版本')
    if current_file.exists() and read(current_file).get('version')==version and read(current_file).get('status') in ('deployed_pending_acceptance','accepted'):return {**read(current_file),'status':'already_current'}
    candidate = read(target/'candidate.json')
    if candidate.get('status') != 'verified' or candidate.get('verification_version')!=2: raise RuntimeError('候选尚未通过完整专项验收')
    if hashlib.sha256(Path(cfg['config_file']).read_bytes()).hexdigest() != candidate['config_sha256']: raise RuntimeError('服务配置已变化，请重新准备候选')
    if not candidate.get('data_compatible') or not candidate.get('rollback_compatible'): raise RuntimeError('数据兼容与旧版恢复尚未确认')
    if candidate.get('base_version')!=installed(cfg):raise RuntimeError('正在运行的版本已变化，请重新验证候选')
    if cfg['environment'] != 'prod' and not cfg['service'].startswith('multica-int-openviking'):raise RuntimeError('隔离实例只能切换专用验收服务')
    if not cfg.get('daemon_health'): raise RuntimeError('缺少 Multica 运行中任务检查入口')
    daemon = cfg['daemon_health'].rstrip('/')
    def control(method,path):
        req=urllib.request.Request(daemon+path,method=method)
        with urllib.request.urlopen(req,timeout=5) as response:return json.load(response)
    before = installed(cfg)
    dropin=Path('/etc/systemd/system')/(cfg['service']+'.d')/'60-multica-version.conf'
    old=dropin.read_bytes() if dropin.exists() else None
    history=Path(cfg['root'])/'deployments/history';history.mkdir(exist_ok=True)
    record=history/(str(time.time_ns())+'.json')
    write(record,{'status':'preparing','before':before,'after':version,'candidate':str(target),'config_sha256':candidate['config_sha256']})
    prior_drain=control('GET','/drain').get('draining',False)
    switched=False
    prior_current=Path(cfg['root'])/'deployments/current.json'
    snapshot={'status':'preparing','before':before,'after':version,'candidate':str(target),'config_sha256':candidate['config_sha256'],'previous_dropin':base64.b64encode(old).decode() if old is not None else None,'previous_current':read(prior_current) if prior_current.exists() else None,'previous_draining':prior_drain}
    write(record,snapshot)
    control('POST','/drain')
    try:
        deadline=time.monotonic()+120
        while time.monotonic()<deadline:
            health=control('GET','/health');drain=control('GET','/drain')
            if drain.get('ready_to_stop') and not health.get('pending_terminal_report_count',0): break
            time.sleep(1)
        else: raise RuntimeError('仍有运行中任务，本次不切换服务')
        dropin.parent.mkdir(exist_ok=True)
        snapshot['status']='switching';write(record,snapshot)
        switched=True
        write(prior_current,{'status':'switching','version':version,'candidate':str(target),'previous_version':before,'record':str(record)})
        command=' '.join(json.dumps(str(arg),ensure_ascii=False).replace('%','%%').replace('$','$$') for arg in [candidate['program'],'--config',cfg['config_file']])
        dropin.write_text('[Service]\nExecStart=\nExecStart='+command+'\n')
        run(['systemctl','daemon-reload']);run(['systemctl','restart',cfg['service']])
        original=read(cfg['config_file']);server=original['server'];url=f'http://127.0.0.1:{server["port"]}';wait_health(url)
        acceptance=check_session_flow(url,'deployment-live-'+str(time.time_ns()),'AmberBridgeLive'+str(time.time_ns()))
        result={'status':'deployed_pending_acceptance','version':version,'previous_version':before,'candidate':str(target),'record':str(record),'native_acceptance':acceptance}
        write(Path(cfg['root'])/'deployments/current.json',result);write(record,{**snapshot,**result});return result
    except Exception as error:
        if not switched:
            write(record,{**snapshot,'status':'not_switched','error':str(error)})
            raise
        if old is None:
            if dropin.exists():dropin.unlink()
        else:dropin.write_bytes(old)
        run(['systemctl','daemon-reload']);run(['systemctl','restart',cfg['service']])
        original=read(cfg['config_file']);wait_health(f'http://127.0.0.1:{original["server"]["port"]}')
        if snapshot['previous_current'] is not None:write(prior_current,snapshot['previous_current'])
        elif prior_current.exists():prior_current.unlink()
        write(record,{**snapshot,'status':'recovered','error':str(error)})
        raise
    finally:
        if not prior_drain:control('DELETE','/drain')


def recover(cfg):
    current=Path(cfg['root'])/'deployments/current.json'
    if not current.exists():raise RuntimeError('没有可恢复的部署记录')
    state=read(current);record=Path(state['record']);operation=read(record)
    if 'previous_dropin' not in operation:raise RuntimeError('该部署记录没有可恢复的旧版本快照')
    daemon=cfg['daemon_health'].rstrip('/')
    def control(method,path):
        with urllib.request.urlopen(urllib.request.Request(daemon+path,method=method),timeout=5) as response:return json.load(response)
    draining=control('GET','/drain').get('draining',False);control('POST','/drain')
    try:
        if not control('GET','/drain').get('ready_to_stop'):raise RuntimeError('仍有运行中任务，请在任务结束后恢复')
        dropin=Path('/etc/systemd/system')/(cfg['service']+'.d')/'60-multica-version.conf'
        previous=operation['previous_dropin']
        if previous is None:
            if dropin.exists():dropin.unlink()
        else:dropin.write_bytes(base64.b64decode(previous))
        run(['systemctl','daemon-reload']);run(['systemctl','restart',cfg['service']])
        wait_health('http://127.0.0.1:'+str(read(cfg['config_file'])['server']['port']))
        if operation['previous_current'] is None:current.unlink(missing_ok=True)
        else:write(current,operation['previous_current'])
        result={**operation,'status':'recovered','version':operation['before'],'previous_version':operation['after'],'recovered_at':time.time()};write(record,result);return result
    finally:
        if not draining:control('DELETE','/drain')


def main():
    parser=argparse.ArgumentParser();parser.add_argument('action',choices=['status','prepare','deploy','recover']);parser.add_argument('--config',required=True);parser.add_argument('--version');parser.add_argument('--operation-id');args=parser.parse_args()
    cfg=config(args.config);root=Path(cfg['root'])/'deployments';root.mkdir(exist_ok=True)
    job=root/'jobs'/((args.operation_id or '')+'.json')
    if args.operation_id and not re.fullmatch(r'[a-f0-9-]{36}',args.operation_id):raise RuntimeError('操作身份无效')
    if args.action=='status' and args.operation_id:
        result=read(job) if job.exists() else {'state':'missing'}
        if result.get('state')=='running':
            try:
                command=Path('/proc/'+str(result['pid'])+'/cmdline').read_bytes()
                if args.operation_id.encode() not in command:result.update(state='interrupted')
            except FileNotFoundError:result.update(state='interrupted')
        print(json.dumps(result,ensure_ascii=False));return
    with (root/'operation.lock').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        if args.operation_id:write(job,{'state':'running','pid':os.getpid(),'action':args.action,'started_at':time.time()})
        try:
            if args.action=='status':result={'current_version':installed(cfg),'latest':None,'candidates':[read(p) for p in (root/'versions').glob('*/candidate.json')]}
            elif args.action=='prepare':result=prepare(cfg,args.version or source_info()['version'])
            elif args.action=='recover':result=recover(cfg)
            else:result=deploy(cfg,args.version or '')
            if args.operation_id:write(job,{'state':'completed','result':result})
        except Exception as error:
            if args.operation_id:write(job,{'state':'failed','error':str(error)})
            raise
        print(json.dumps(result,ensure_ascii=False))

if __name__=='__main__':main()
