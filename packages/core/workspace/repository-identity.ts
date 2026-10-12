import type { WorkspaceRepo, WorkspaceRepoProvider } from '../types/workspace';

export function repositoryIdentity(raw: string): string | null {
 const value=raw.trim(); if(!value)return null;
 const scp=!value.includes('://') ? value.match(/^(?:[^@\s/]+@)?([^:\s/]+):(.+)$/) : null;
 try {
  const u=new URL(scp ? `ssh://${scp[1]}/${scp[2]}` : value);
  if(!['https:','http:','ssh:','git:'].includes(u.protocol)||!u.hostname)return null;
  let path=u.pathname.replace(/^\/+|\/+$/g,'').replace(/\.git$/i,'');
  if(!path)return null;
  if(u.hostname.toLowerCase()==='github.com')path=path.toLowerCase();
  return `${u.hostname.toLowerCase()}/${path}`;
 } catch {return null;}
}
export function repositoryProvider(repo: WorkspaceRepo): WorkspaceRepoProvider {
 if(repo.provider) return repo.provider==='github'||repo.provider==='gongfeng'?repo.provider:'unknown';
 const host=repositoryIdentity(repo.url)?.split('/')[0];
 return host==='github.com'?'github':host==='git.code.tencent.com'?'gongfeng':'unknown';
}
export function repositoryStatus(repo: WorkspaceRepo): 'ok'|'error'|'pending' {
 if([repo.connection_status,repo.test_status].some(v=>['failed','error','disconnected'].includes(v ?? '')))return 'error';
 if(repo.test_status==='passed'||['credential_backed','connected'].includes(repo.connection_status ?? ''))return 'ok';
 return 'pending';
}
export function repositoryPath(repo: WorkspaceRepo) { return repo.project_path || repositoryIdentity(repo.url)?.split('/').slice(1).join('/') || repo.url; }
