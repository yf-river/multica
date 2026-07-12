-- Mention links have one current persisted shape:
--   [@Label](mention://member/<id>)
-- Normalize the retired attribute shortcode once, then let every renderer and
-- editor consume the same Markdown without a read-time compatibility parser.
CREATE OR REPLACE FUNCTION normalize_legacy_mention_markdown(input text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    result text := input;
    shortcode text;
    mention_id text;
    mention_label text;
BEGIN
    IF input IS NULL OR input !~ '\[@[[:space:]]+' THEN
        RETURN input;
    END IF;

    FOR shortcode IN
        SELECT match[1]
        FROM regexp_matches(input, '(\[@[[:space:]]+[^]]*\])', 'g') AS match
    LOOP
        mention_id := (regexp_match(shortcode, '[[:space:]]id="([^"]*)"'))[1];
        mention_label := (regexp_match(shortcode, '[[:space:]]label="([^"]*)"'))[1];
        IF COALESCE(mention_id, '') <> '' AND COALESCE(mention_label, '') <> '' THEN
            result := replace(
                result,
                shortcode,
                '[@' || mention_label || '](mention://member/' || mention_id || ')'
            );
        END IF;
    END LOOP;
    RETURN result;
END;
$$;

UPDATE issue
SET description = normalize_legacy_mention_markdown(description)
WHERE description ~ '\[@[[:space:]]+';

UPDATE comment
SET content = normalize_legacy_mention_markdown(content)
WHERE content ~ '\[@[[:space:]]+';

UPDATE chat_message
SET content = normalize_legacy_mention_markdown(content)
WHERE content ~ '\[@[[:space:]]+';

DROP FUNCTION normalize_legacy_mention_markdown(text);
