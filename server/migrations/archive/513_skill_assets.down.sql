DROP TABLE IF EXISTS skill_asset;
ALTER TABLE skill_file DROP COLUMN mode, DROP COLUMN sha256, DROP COLUMN size_bytes, DROP COLUMN content_type, DROP COLUMN storage_key, DROP COLUMN kind;
