ALTER TABLE resource_create_request
    DROP CONSTRAINT resource_create_request_resource_type_check;

ALTER TABLE resource_create_request
    ADD CONSTRAINT resource_create_request_resource_type_check
    CHECK (resource_type IN (
        'workspace', 'workspace_member', 'project', 'squad', 'agent', 'skill',
        'attachment', 'quick_create', 'issue', 'comment', 'autopilot_trigger',
        'issue_rerun', 'runtime_profile', 'label',
        'prompt_library_item', 'prompt_library_version', 'prompt_library_trial',
        'agent_playground_experiment', 'prompt_evaluation_agent_run',
        'prompt_evaluation_local_run', 'prompt_evaluation_re_eval_asset',
        'prompt_evaluation_candidate', 'prompt_evaluation_candidate_publish',
        'prompt_evaluation_candidate_reject'
    ));
