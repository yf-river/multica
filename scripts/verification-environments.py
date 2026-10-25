#!/usr/bin/env python3
"""固定验收槽位安装：只创建专属资源，默认不启用交付调度。"""
import base64
import argparse
import importlib.util
import json
import os
from pathlib import Path
import pwd
import secrets
import shutil
import socket
import subprocess
import urllib.request

spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)


def free_port():
    with socket.socket() as stream:
        stream.bind(('127.0.0.1',0))
        return stream.getsockname()[1]


def account(name,home):
    try:
        item=pwd.getpwnam(name)
    except KeyError:
        r.run(['useradd','--system','--user-group','--home-dir',home,'--shell','/usr/sbin/nologin',name])
        item=pwd.getpwnam(name)
    if item.pw_uid==0 or Path(item.pw_dir)!=home:
        raise RuntimeError('执行用户归属不匹配')
    home.mkdir(parents=True,exist_ok=True);home.chmod(0o700)
    os.chown(home,item.pw_uid,item.pw_gid)
    return item


def api(url,route,body,token=None):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    request=urllib.request.Request(url+route,data=json.dumps(body).encode(),headers=headers)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=30) as response:
        return json.load(response)


def install(env,artifact,postgres_config,root,host,codex_auth=None,codex_model=None):
    if env not in r.VERIFICATION_ENVS:raise RuntimeError('只能安装固定验收槽位')
    if os.environ.get('MULTICA_TASK_ID'):raise RuntimeError('安装须由外部维护身份执行')
    if r.meta_path(env).exists() or r.config_path(env).exists():
        raise RuntimeError('槽位已有配置，拒绝覆盖；请先检查已有安装记录')
    manifest=r.read(artifact/'release.json')
    if r.checksums(artifact)!=manifest['files']:raise RuntimeError('构建产物校验失败')
    pg=r.read(postgres_config)
    # PostgreSQL 管理连接仅能指向独立验收实例；不能指定正式实例。
    container=pg['container']
    inspected=json.loads(r.output(['docker','inspect',container]))[0]
    if inspected['Config'].get('Labels',{}).get('multica.purpose')!='verification-pool':
        raise RuntimeError('数据库实例缺少验收池归属标记')
    ports=inspected['NetworkSettings']['Ports']['5432/tcp']
    if len(ports)!=1 or ports[0]['HostIp']!='127.0.0.1':raise RuntimeError('验收数据库必须仅绑定回环地址')
    pgport=ports[0]['HostPort']
    slotroot=(root/env).resolve()
    if slotroot.exists():raise RuntimeError('槽位数据目录已存在，拒绝覆盖')
    slotroot.mkdir(parents=True,mode=0o711)
    app=account('multica-'+env+'-app',slotroot/'app')
    agent=account('multica-'+env+'-agent',slotroot/'agent')
    dbname='multica_'+env.replace('-','_');dbuser=env.replace('-','_');dbpass=secrets.token_hex(24)
    statement=f"CREATE ROLE {dbuser} LOGIN PASSWORD '{dbpass}' NOSUPERUSER NOCREATEDB NOCREATEROLE;\nCREATE DATABASE {dbname} OWNER {dbuser};\nREVOKE CONNECT ON DATABASE {dbname} FROM PUBLIC;\nGRANT CONNECT ON DATABASE {dbname} TO {dbuser};\n"
    # SQL 经 stdin 送入已验证的专属容器，密码不进入进程参数或输出。
    subprocess.run(['docker','exec','-i',container,'psql','-U','postgres','-v','ON_ERROR_STOP=1','-d','postgres'],input=statement,text=True,check=True,stdout=subprocess.DEVNULL)
    ports=[]
    while len(ports)<3:
        port=free_port()
        if port not in ports:ports.append(port)
    backend,frontend,pprof=map(str,ports)
    front=f'http://{host}:{frontend}';back=f'http://127.0.0.1:{backend}'
    base={'PATH':'/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/bin','LANG':'C.UTF-8','MULTICA_ENV_ID':env}
    appbase={**base,'HOME':str(slotroot/'app'),'USER':app.pw_name,'MULTICA_SERVICE_USER':app.pw_name,'TMPDIR':str(slotroot/'app'/'tmp')}
    agentbase={**base,'HOME':str(slotroot/'agent'),'USER':agent.pw_name,'MULTICA_SERVICE_USER':agent.pw_name,'TMPDIR':str(slotroot/'agent'/'tmp')}
    for owner,folders in [(app,['tmp','uploads']),(agent,['tmp','workspaces','codebuddy','codex'])]:
        for folder in folders:
            path=Path(owner.pw_dir)/folder;path.mkdir(mode=0o700);os.chown(path,owner.pw_uid,owner.pw_gid)
    settings=slotroot/'agent'/'codebuddy'/'settings.json'
    settings.write_text(json.dumps({'model':'deepseek-v4.1-flash'})+'\n')
    settings.chmod(0o600);os.chown(settings,agent.pw_uid,agent.pw_gid)
    password=secrets.token_urlsafe(24)
    cfg=dict(server={**appbase,'MULTICA_MIGRATION_BASELINE':'true','DATABASE_URL':f'postgres://{dbuser}:{dbpass}@127.0.0.1:{pgport}/{dbname}','PORT':backend,'PPROF_PORT':pprof,'JWT_SECRET':secrets.token_hex(32),'MULTICA_EXTERNAL_CREDENTIAL_KEY':base64.b64encode(secrets.token_bytes(32)).decode(),'FRONTEND_ORIGIN':front,'CORS_ALLOWED_ORIGINS':front,'MULTICA_APP_URL':front,'ALLOW_SIGNUP':'true','ALLOWED_ACCOUNTS':'verification','LOCAL_UPLOAD_DIR':str(slotroot/'app'/'uploads'),'LOCAL_UPLOAD_BASE_URL':front+'/uploads'},web={**appbase,'PORT':frontend,'HOSTNAME':'0.0.0.0','REMOTE_API_URL':back},daemon={**agentbase,'MULTICA_SERVER_URL':back,'MULTICA_APP_URL':front,'MULTICA_ENV_WRITABLE_ROOT':str(slotroot/'agent'/'workspaces'),'MULTICA_WORKSPACES_ROOT':str(slotroot/'agent'/'workspaces'),'CODEBUDDY_CONFIG_DIR':str(slotroot/'agent'/'codebuddy'),'CODEX_HOME':str(slotroot/'agent'/'codex'),'MULTICA_DAEMON_AUTO_UPDATE':'false','MULTICA_DAEMON_AUTO_RELOAD':'false'})
    if codex_auth:
        authfile=slotroot/'agent'/'codex'/'auth.json'
        authfile.write_bytes(codex_auth.read_bytes());authfile.chmod(0o600);os.chown(authfile,agent.pw_uid,agent.pw_gid)
        modelconfig=slotroot/'agent'/'codex'/'config.toml'
        modelconfig.write_text('model = '+json.dumps(codex_model)+'\n');modelconfig.chmod(0o600);os.chown(modelconfig,agent.pw_uid,agent.pw_gid)
        cfg['daemon']['MULTICA_CODEX_PATH']='/usr/local/sbin/codex'
    r.validate_verification_config(env,cfg)
    release=r.RUN/'runtime'/env/'a';r.copy_tree(artifact,release)
    r.write(r.config_path(env),cfg,True)
    r.write(r.RUN/'verification-install'/f'{env}.json',dict(environment=env,stage='initializing',data_root=str(slotroot),database=dbname,account='verification',password=password),True)
    r.run([release/'server/bin/migrate','up'],cwd=release/'server',env={**cfg['server'],'MULTICA_MIGRATION_BASELINE':'true'})
    logs={role:str(r.RUN/'logs'/env/(role+'.log')) for role in ('server','web','daemon')}
    (r.RUN/'logs'/env).mkdir(parents=True,exist_ok=True)
    server_pid=r.start([release/'server/bin/server'],release,cfg['server'],logs['server'])
    r.ready(back+'/health',pid=server_pid)
    login=api(back,'/auth/login',dict(account='verification',password=password))
    workspace=api(back,'/api/workspaces',dict(name='任务验收',slug='verification'),login['token'])
    api(back,'/api/me/onboarding/complete',dict(completion_path='skip_existing',workspace_id=workspace['id']),login['token'])
    profile='goal-test-'+env
    profile_path=slotroot/'agent'/'.multica'/'profiles'/profile/'config.json'
    r.write(profile_path,dict(server_url=back,app_url=front,workspace_id=workspace['id'],token=login['token'],workspaces_root=cfg['daemon']['MULTICA_WORKSPACES_ROOT'],disable_auto_update=True,disable_auto_reload=True,max_concurrent_tasks=1),True)
    for path in [slotroot/'agent'/'.multica',slotroot/'agent'/'.multica'/'profiles',profile_path.parent,profile_path]:os.chown(path,agent.pw_uid,agent.pw_gid)
    r.owned_stop(server_pid,'server',release)
    metadata=dict(environment=env,backend_url=back,frontend_url=front,backend_port=backend,frontend_port=frontend,release_id=manifest['id'],release_dir=str(release),commit=manifest['source_commit'],source_digest=manifest['source_digest'],daemon_profile=profile,daemon_profile_path=str(profile_path),daemon_workspaces_root=cfg['daemon']['MULTICA_WORKSPACES_ROOT'],runtime_config_file=str(r.config_path(env)),log_paths=logs,deployed_at=manifest['built_at'])
    r.write(r.meta_path(env),metadata,True)
    # 先检查 health 端口，避免 profile 哈希碰撞停止其他 Daemon。
    import urllib.parse
    healthport=urllib.parse.urlparse(r.health_url(env)).port
    with socket.socket() as probe:probe.bind(('127.0.0.1',healthport))
    try:
        metadata['pids']=r.launch(env,release,cfg,metadata)
    finally:
        r.write(r.meta_path(env),metadata,True)
    verify_installed(env)

def verify_installed(env):
    # 安装中断后只核对本槽位已有资源，不重建数据库或覆盖凭据。
    metadata=r.read(r.meta_path(env));cfg=r.read(r.config_path(env))
    r.validate_verification_config(env,cfg)
    r.check_process_ownership(env,metadata)
    profile=r.read(Path(metadata['daemon_profile_path']))
    api(metadata['backend_url'],'/api/me/onboarding/complete',dict(completion_path='skip_existing',workspace_id=profile['workspace_id']),profile['token'])
    r.verify_browser(env)
    record_path=r.RUN/'verification-install'/f'{env}.json'
    record=r.read(record_path);record['stage']='installed';r.write(record_path,record,True)
    print(json.dumps(dict(environment=env,frontend_url=metadata['frontend_url'],status='installed',pool_enabled=False)))


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--environment',choices=r.VERIFICATION_ENVS,required=True)
    parser.add_argument('--verify-existing',action='store_true')
    parser.add_argument('--artifact',type=Path)
    parser.add_argument('--postgres-config',type=Path)
    parser.add_argument('--data-root',type=Path)
    parser.add_argument('--public-host')
    parser.add_argument('--codex-auth-file',type=Path)
    parser.add_argument('--codex-model')
    args=parser.parse_args()
    if bool(args.codex_auth_file)!=bool(args.codex_model):parser.error('model and auth file must be provided together')
    if args.verify_existing:
        verify_installed(args.environment);raise SystemExit(0)
    if not all((args.artifact,args.postgres_config,args.data_root,args.public_host)):
        parser.error('installation requires artifact, postgres config, data root and public host')
    install(args.environment,args.artifact.resolve(),args.postgres_config,args.data_root.resolve(),args.public_host,args.codex_auth_file,args.codex_model)
