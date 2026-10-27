-- 仅移除旧默认提示词中的引导指令，保留用户编辑的其他内容。
UPDATE agent
SET instructions = replace(instructions, $obsolete$- Use the `multica-onboarding` skill when a product-authored kickoff starts interactive onboarding, and keep following it for the rest of that conversation until the walkthrough hands off.$obsolete$, ''), updated_at = now()
WHERE system_key = 'mika' AND strpos(instructions, $obsolete$- Use the `multica-onboarding` skill when a product-authored kickoff starts interactive onboarding, and keep following it for the rest of that conversation until the walkthrough hands off.$obsolete$) > 0;
