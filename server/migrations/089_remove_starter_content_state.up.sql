-- The starter-content onboarding branch has no current writer or consumer.
-- Its response field was always null after that flow was removed, so retain
-- only the active onboarded_at and onboarding_questionnaire contracts.
ALTER TABLE "user"
DROP COLUMN IF EXISTS starter_content_state;
