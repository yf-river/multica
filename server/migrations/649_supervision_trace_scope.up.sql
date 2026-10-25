CREATE OR REPLACE VIEW supervision_trace_scope AS
SELECT id,workspace_id,issue_id,status,material_cutoff,preparation_started_at,false AS manual FROM supervision_round
UNION ALL
SELECT id,workspace_id,issue_id,status,material_cutoff,preparation_started_at,true AS manual FROM supervision_manual_preparation;
