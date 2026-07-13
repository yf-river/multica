-- Restore the attachment URL form if this data migration is rolled back.
DO $$
DECLARE
    attachment_row record;
    old_marker text;
    new_marker text;
    escaped_filename text;
    source_content text;
    converted_content text;
BEGIN
    FOR attachment_row IN
        SELECT id, issue_id, comment_id, chat_message_id, filename, url
        FROM attachment
        WHERE url ~* '^https?://'
          AND split_part(split_part(url, '?', 1), '#', 1)
              !~* E'\\.(png|jpe?g|gif|webp|svg|ico|bmp|tiff?)$'
    LOOP
        escaped_filename := replace(attachment_row.filename, E'\\', E'\\\\');
        escaped_filename := replace(escaped_filename, '[', E'\\[');
        escaped_filename := replace(escaped_filename, ']', E'\\]');
        escaped_filename := replace(escaped_filename, '(', E'\\(');
        escaped_filename := replace(escaped_filename, ')', E'\\)');
        new_marker := format(
            '!file[%s](/api/attachments/%s/download)',
            escaped_filename,
            attachment_row.id
        );
        old_marker := format('[%s](%s)', attachment_row.filename, attachment_row.url);

        IF attachment_row.comment_id IS NOT NULL THEN
            SELECT content INTO source_content
            FROM comment
            WHERE id = attachment_row.comment_id;
        ELSIF attachment_row.chat_message_id IS NOT NULL THEN
            SELECT content INTO source_content
            FROM chat_message
            WHERE id = attachment_row.chat_message_id;
        ELSIF attachment_row.issue_id IS NOT NULL THEN
            SELECT description INTO source_content
            FROM issue
            WHERE id = attachment_row.issue_id;
        ELSE
            CONTINUE;
        END IF;

        SELECT string_agg(
            CASE
                WHEN btrim(line, E' \t\r\f\v') = new_marker THEN old_marker
                ELSE line
            END,
            E'\n' ORDER BY ordinal
        )
        INTO converted_content
        FROM unnest(string_to_array(source_content, E'\n'))
            WITH ORDINALITY AS lines(line, ordinal);

        IF converted_content IS DISTINCT FROM source_content THEN
            IF attachment_row.comment_id IS NOT NULL THEN
                UPDATE comment SET content = converted_content
                WHERE id = attachment_row.comment_id;
            ELSIF attachment_row.chat_message_id IS NOT NULL THEN
                UPDATE chat_message SET content = converted_content
                WHERE id = attachment_row.chat_message_id;
            ELSE
                UPDATE issue SET description = converted_content
                WHERE id = attachment_row.issue_id;
            END IF;
        END IF;
    END LOOP;
END
$$;
