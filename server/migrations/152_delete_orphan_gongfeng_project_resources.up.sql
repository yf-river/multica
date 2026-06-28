DELETE FROM project_resource pr
USING workspace w
WHERE pr.workspace_id = w.id
  AND pr.resource_type = 'gongfeng_repo'
  AND NOT EXISTS (
    SELECT 1
    FROM jsonb_array_elements(COALESCE(w.repos, '[]'::jsonb)) AS repo(value)
    WHERE COALESCE(
      NULLIF(BTRIM(repo.value->>'project_path', '/ '), ''),
      CASE
        WHEN repo.value->>'url' LIKE 'http%://git.code.tencent.com/%' THEN
          regexp_replace(
            regexp_replace(
              regexp_replace(repo.value->>'url', '^https?://git\.code\.tencent\.com/', ''),
              '/(-/)?(tree|commits|commit|tags|merge_requests|blob)/.*$',
              ''
            ),
            '/$',
            ''
          )
        ELSE ''
      END
    ) = COALESCE(
      NULLIF(BTRIM(pr.resource_ref->>'project_path', '/ '), ''),
      CASE
        WHEN pr.resource_ref->>'url' LIKE 'http%://git.code.tencent.com/%' THEN
          regexp_replace(
            regexp_replace(
              regexp_replace(pr.resource_ref->>'url', '^https?://git\.code\.tencent\.com/', ''),
              '/(-/)?(tree|commits|commit|tags|merge_requests|blob)/.*$',
              ''
            ),
            '/$',
            ''
          )
        ELSE ''
      END
    )
  );
