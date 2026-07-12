ALTER TABLE resource_create_request
    DROP CONSTRAINT resource_create_request_resource_type_check;

ALTER TABLE resource_create_request
    ADD CONSTRAINT resource_create_request_resource_type_check
    CHECK (resource_type IN ('project', 'squad', 'agent', 'skill', 'attachment', 'quick_create'));

DROP INDEX idx_issue_origin;
CREATE UNIQUE INDEX idx_issue_origin
    ON issue (workspace_id, origin_type, origin_id)
    WHERE origin_type IS NOT NULL;
