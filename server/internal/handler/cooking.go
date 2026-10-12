package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/cooking"
)

func (h *Handler) cookingFavorites(w http.ResponseWriter, r *http.Request) (map[string]bool, bool) {
	user, ok := requireUserID(w, r)
	if !ok {
		return nil, false
	}
	rows, err := h.DB.Query(r.Context(), `SELECT recipe_id FROM cooking_favorite WHERE user_id=$1`, user)
	if err != nil {
		writeError(w, 500, "Unable to load favorites")
		return nil, false
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			writeError(w, 500, "Unable to load favorites")
			return nil, false
		}
		result[id] = true
	}
	if rows.Err() != nil {
		writeError(w, 500, "Unable to load favorites")
		return nil, false
	}
	return result, true
}
func (h *Handler) CookingCategories(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, cooking.Default.Categories())
}
func (h *Handler) CookingList(w http.ResponseWriter, r *http.Request) {
	favorites, ok := h.cookingFavorites(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	kind := "recipe"
	// The registered route determines intent, never a client-supplied owner.
	if r.URL.Path == "/api/cooking/tips" {
		kind = "tip"
	}
	filter := cooking.Filter{Kind: kind, Query: q.Get("q"), Category: q.Get("category"), Favorites: r.URL.Path == "/api/cooking/favorites", Page: page}
	writeJSON(w, 200, cooking.Default.Search(filter, favorites))
}
func (h *Handler) CookingDetail(w http.ResponseWriter, r *http.Request) {
	e, ok := cooking.Default.Get(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, 404, "Cooking content not found")
		return
	}
	if (e.Kind == "tip") != (strings.HasPrefix(r.URL.Path, "/api/cooking/tips/")) {
		writeError(w, 404, "Cooking content not found")
		return
	}
	favorites, ok := h.cookingFavorites(w, r)
	if !ok {
		return
	}
	e.Favorite = favorites[e.ID]
	related := cooking.Default.Search(cooking.Filter{Kind: e.Kind, Category: e.Category}, favorites).Items
	filtered := []cooking.Entry{}
	for _, x := range related {
		if x.ID != e.ID && len(filtered) < 4 {
			filtered = append(filtered, x)
		}
	}
	writeJSON(w, 200, map[string]any{"entry": e, "related": filtered, "version": cooking.Default.Version})
}
func (h *Handler) CookingFavorite(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, user, "user id")
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodPut {
		e, exists := cooking.Default.Get(id)
		if !exists || e.Kind != "recipe" {
			writeError(w, 404, "Recipe not found")
			return
		}
		if _, err := h.DB.Exec(r.Context(), `INSERT INTO cooking_favorite(user_id,recipe_id) VALUES($1,$2) ON CONFLICT(user_id,recipe_id) DO NOTHING`, userID, id); err != nil {
			writeError(w, 500, "Unable to save favorite")
			return
		}
	} else {
		if _, err := h.DB.Exec(r.Context(), `DELETE FROM cooking_favorite WHERE user_id=$1 AND recipe_id=$2`, userID, id); err != nil {
			writeError(w, 500, "Unable to remove favorite")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
