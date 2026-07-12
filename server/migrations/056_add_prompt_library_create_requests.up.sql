ALTER TABLE resource_create_request
    DROP CONSTRAINT resource_create_request_resource_type_check;

ALTER TABLE resource_create_request
    ADD CONSTRAINT resource_create_request_resource_type_check
    CHECK (resource_type IN ('project', 'squad', 'agent', 'skill', 'attachment', 'quick_create', 'issue', 'comment', 'prompt_library_item', 'prompt_library_version', 'prompt_library_trial'));
