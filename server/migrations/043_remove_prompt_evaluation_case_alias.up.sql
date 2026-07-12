-- `cases` is the sole current case collection. The former `用例` mirror had
-- no production reader and was written alongside `cases`, so remove it once.
UPDATE prompt_evaluation_asset
SET payload = payload - '用例'
WHERE payload ? '用例';
