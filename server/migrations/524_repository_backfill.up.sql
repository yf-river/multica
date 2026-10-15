-- 保留迁移前配置，供发布恢复使用；正常业务只读取统一仓库。
INSERT INTO repository_migration_backup(kind,id,data)
SELECT 'workspace',id,jsonb_build_object('repos',repos) FROM workspace w
WHERE NOT EXISTS (SELECT 1 FROM repository_migration_backup b WHERE b.kind='workspace' AND b.id=w.id);
INSERT INTO repository_migration_backup(kind,id,data)
SELECT 'project_resource',id,to_jsonb(p) FROM project_resource p
WHERE resource_type IN ('github_repo','gongfeng_repo') AND resource_ref ? 'url'
AND NOT EXISTS (SELECT 1 FROM repository_migration_backup b WHERE b.kind='project_resource' AND b.id=p.id);

CREATE OR REPLACE FUNCTION pg_temp.repository_identity(raw text) RETURNS text LANGUAGE plpgsql AS $$
DECLARE value text; host text; path text;
BEGIN
 value := trim(raw);
 IF position('://' in value)=0 THEN
   value := regexp_replace(value,'^[^@]+@','');
   value := regexp_replace(value,'^([^:]+):','\1/');
 ELSE
   value := regexp_replace(value,'^[^:]+://','');
   value := regexp_replace(value,'^[^/@]+@','');
 END IF;
 host := lower(split_part(value,'/',1));
 path := regexp_replace(substring(value from position('/' in value)+1),'[?#].*$','');
 path := regexp_replace(trim(both '/' from path),'\.git$','');
 IF host='github.com' THEN path:=lower(path); END IF;
 IF host='git.code.tencent.com' THEN path:=regexp_replace(split_part(path,'/-/',1),'\.git$',''); END IF;
 RETURN host || '/' || path;
END $$;

INSERT INTO repository(workspace_id,identity,config,default_for_tasks)
SELECT w.id,pg_temp.repository_identity(r->>'url'),CASE WHEN r->>'url' LIKE '%git.code.tencent.com/%/-/%' THEN jsonb_set(r,'{url}',to_jsonb(regexp_replace(r->>'url','/-/.*$','.git'))) ELSE r END,true
FROM workspace w CROSS JOIN LATERAL jsonb_array_elements(COALESCE(w.repos,'[]')) r
WHERE COALESCE(r->>'url','')<>''
ON CONFLICT (workspace_id,identity) DO NOTHING;
INSERT INTO repository(workspace_id,identity,config)
SELECT p.workspace_id,pg_temp.repository_identity(p.resource_ref->>'url'),
 jsonb_build_object('url',CASE WHEN p.resource_type='gongfeng_repo' THEN regexp_replace(p.resource_ref->>'url','/-/.*$','.git') ELSE p.resource_ref->>'url' END,'provider',CASE p.resource_type WHEN 'github_repo' THEN 'github' ELSE 'gongfeng' END)
FROM project_resource p WHERE p.resource_type IN ('github_repo','gongfeng_repo') AND COALESCE(p.resource_ref->>'url','')<>''
ON CONFLICT (workspace_id,identity) DO NOTHING;
UPDATE project_resource p SET resource_ref=jsonb_strip_nulls(jsonb_build_object(
 'repository_id',r.id,'ref',COALESCE(NULLIF(p.resource_ref->>'branch',''),NULLIF(p.resource_ref->>'ref',''))))
FROM repository r WHERE p.workspace_id=r.workspace_id
 AND p.resource_type IN ('github_repo','gongfeng_repo')
 AND pg_temp.repository_identity(p.resource_ref->>'url')=r.identity;
