CREATE INDEX idx_resource_create_request_completed_at
    ON resource_create_request (completed_at)
    WHERE completed_at IS NOT NULL;

CREATE INDEX idx_resource_create_request_incomplete_created_at
    ON resource_create_request (created_at)
    WHERE completed_at IS NULL;
