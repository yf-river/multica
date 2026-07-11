-- Current quick-create projections store the issue title directly. Normalize
-- rows written by the retired projection format so clients no longer need to
-- parse "Created <identifier>: <title>" at render time.
UPDATE inbox_item
SET title = COALESCE(
    NULLIF(
        btrim(regexp_replace(
            title,
            '^Created[[:space:]]+[A-Z][A-Z0-9]*-[0-9]+:[[:space:]]*',
            '',
            'i'
        )),
        ''
    ),
    NULLIF(
        regexp_replace(
            btrim(COALESCE(details ->> 'original_prompt', '')),
            '[[:space:]]+',
            ' ',
            'g'
        ),
        ''
    ),
    title
)
WHERE type = 'quick_create_done'
  AND title ~* '^Created[[:space:]]+[A-Z][A-Z0-9]*-[0-9]+:[[:space:]]*';
