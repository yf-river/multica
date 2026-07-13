-- Replace persisted raw storage URLs with the one durable attachment path.
-- Every current writer stores markdown_url; this migration lets readers stop
-- matching attachment.url as a second historical format.
DO $$
DECLARE
    attachment_row record;
    stable_url text;
BEGIN
    FOR attachment_row IN
        SELECT id, issue_id, comment_id, chat_message_id, url
        FROM attachment
        WHERE url <> ''
    LOOP
        stable_url := format('/api/attachments/%s/download', attachment_row.id);

        IF attachment_row.comment_id IS NOT NULL THEN
            UPDATE comment
            SET content = replace(content, attachment_row.url, stable_url)
            WHERE id = attachment_row.comment_id
              AND strpos(content, attachment_row.url) > 0;
        ELSIF attachment_row.chat_message_id IS NOT NULL THEN
            UPDATE chat_message
            SET content = replace(content, attachment_row.url, stable_url)
            WHERE id = attachment_row.chat_message_id
              AND strpos(content, attachment_row.url) > 0;
        ELSIF attachment_row.issue_id IS NOT NULL THEN
            UPDATE issue
            SET description = replace(description, attachment_row.url, stable_url)
            WHERE id = attachment_row.issue_id
              AND strpos(description, attachment_row.url) > 0;
        END IF;
    END LOOP;
END
$$;
