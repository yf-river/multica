ALTER TABLE public.life_experiment_round
    DROP CONSTRAINT life_experiment_round_review_draft_check;
ALTER TABLE public.life_experiment_round
    ADD CONSTRAINT life_experiment_round_review_draft_check CHECK (
        review_draft IS NULL OR jsonb_typeof(review_draft) = 'object'
    );
