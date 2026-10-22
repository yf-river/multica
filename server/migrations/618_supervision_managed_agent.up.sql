-- 质量监督改为工作区托管：原来由用户手工创建的监督 Agent 标记为产品托管，
-- 不再出现在 Agent 列表、指派和提及选择中，但仍保留运行时绑定、运行历史和
-- 任务归属。supervision_config.instructions 接管提示词，Agent 的 instructions
-- 不再参与监督运行。
UPDATE agent a
SET system_key = 'quality_supervision', updated_at = now()
FROM supervision_config c
WHERE c.agent_id = a.id AND c.workspace_id = a.workspace_id
  AND a.system_key IS NULL;
