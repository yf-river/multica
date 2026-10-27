CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS cooking_favorite_user_recipe_idx ON cooking_favorite(user_id, recipe_id);
