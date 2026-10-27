-- 验收完成前暂停仓库配置写入，保证旧配置仍能准确恢复。
CREATE TABLE repository_migration_state (accepted boolean NOT NULL DEFAULT false);
INSERT INTO repository_migration_state DEFAULT VALUES;
CREATE FUNCTION guard_repository_migration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS (SELECT 1 FROM repository_migration_state WHERE NOT accepted) THEN
   IF TG_TABLE_NAME <> 'project_resource' THEN
     RAISE EXCEPTION '仓库配置迁移正在验收，请稍后修改' USING ERRCODE='55000';
   END IF;
   IF (TG_OP <> 'INSERT' AND OLD.resource_type IN ('github_repo','gongfeng_repo'))
      OR (TG_OP <> 'DELETE' AND NEW.resource_type IN ('github_repo','gongfeng_repo')) THEN
     RAISE EXCEPTION '仓库配置迁移正在验收，请稍后修改' USING ERRCODE='55000';
   END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER repository_migration_guard BEFORE INSERT OR UPDATE OR DELETE ON repository
FOR EACH ROW EXECUTE FUNCTION guard_repository_migration();
CREATE TRIGGER repository_location_migration_guard BEFORE INSERT OR UPDATE OR DELETE ON repository_location
FOR EACH ROW EXECUTE FUNCTION guard_repository_migration();
CREATE TRIGGER project_repository_migration_guard BEFORE INSERT OR UPDATE OR DELETE ON project_resource
FOR EACH ROW EXECUTE FUNCTION guard_repository_migration();
