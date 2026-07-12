ALTER TABLE prompt_library_version
DROP CONSTRAINT prompt_library_version_source_check;

ALTER TABLE prompt_library_version
ADD CONSTRAINT prompt_library_version_source_check
CHECK (source IN ('手动创建', '手动更新', '优化候选发布', '历史回填'));
