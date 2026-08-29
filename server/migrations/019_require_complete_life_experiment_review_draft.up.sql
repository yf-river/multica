UPDATE public.life_experiment_round
SET review_draft = NULL,
    updated_at = now()
WHERE review_draft IS NOT NULL
  AND (
      jsonb_typeof(review_draft->'outcome') IS DISTINCT FROM 'string'
      OR length(btrim(review_draft->>'outcome')) = 0
      OR jsonb_typeof(review_draft->'feelings') IS DISTINCT FROM 'string'
      OR length(btrim(review_draft->>'feelings')) = 0
      OR jsonb_typeof(review_draft->'burden') IS DISTINCT FROM 'string'
      OR length(btrim(review_draft->>'burden')) = 0
      OR jsonb_typeof(review_draft->'companion_correction') IS DISTINCT FROM 'string'
      OR length(btrim(review_draft->>'companion_correction')) = 0
      OR jsonb_typeof(review_draft->'module_proposal') IS DISTINCT FROM 'object'
  );

ALTER TABLE public.life_experiment_round
    DROP CONSTRAINT life_experiment_round_review_draft_check;
ALTER TABLE public.life_experiment_round
    ADD CONSTRAINT life_experiment_round_review_draft_check CHECK (
        review_draft IS NULL OR (
            jsonb_typeof(review_draft) = 'object'
            AND jsonb_typeof(review_draft->'outcome') IS NOT DISTINCT FROM 'string'
            AND length(btrim(review_draft->>'outcome')) > 0
            AND jsonb_typeof(review_draft->'feelings') IS NOT DISTINCT FROM 'string'
            AND length(btrim(review_draft->>'feelings')) > 0
            AND jsonb_typeof(review_draft->'burden') IS NOT DISTINCT FROM 'string'
            AND length(btrim(review_draft->>'burden')) > 0
            AND jsonb_typeof(review_draft->'companion_correction') IS NOT DISTINCT FROM 'string'
            AND length(btrim(review_draft->>'companion_correction')) > 0
            AND jsonb_typeof(review_draft->'module_proposal') IS NOT DISTINCT FROM 'object'
        )
    );
