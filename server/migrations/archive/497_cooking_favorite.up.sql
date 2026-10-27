CREATE TABLE IF NOT EXISTS cooking_favorite (
    user_id uuid NOT NULL,
    recipe_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
