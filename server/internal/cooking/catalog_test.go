package cooking

import "testing"

func TestSearch(t *testing.T) {
	c := &Catalog{Version: "test", Entries: []Entry{
		{ID: "text", Name: "甲", Kind: "recipe", Markdown: "鸡蛋", DifficultyRank: 1},
		{ID: "ingredient", Name: "乙", Kind: "recipe", Ingredients: "鸡蛋", DifficultyRank: 2},
		{ID: "title", Name: "鸡蛋", Kind: "recipe", DifficultyRank: 0},
	}}
	got := c.Search(Filter{Kind: "recipe", Query: "鸡蛋"}, nil)
	if got.Total != 3 || got.Items[0].ID != "title" || got.Items[1].ID != "ingredient" {
		t.Fatalf("search ranking: %+v", got)
	}
	sorted := c.Search(Filter{Kind: "recipe"}, nil)
	if sorted.Items[0].ID != "ingredient" || sorted.Items[1].ID != "text" || sorted.Items[2].ID != "title" {
		t.Fatalf("default name order: %+v", sorted.Items)
	}
	if c.Search(Filter{Kind: "recipe", Page: 999}, nil).Total != 3 {
		t.Fatal("pagination loses total")
	}
	if len(c.Search(Filter{Kind: "recipe", Favorites: true}, map[string]bool{}).Items) != 0 {
		t.Fatal("favorites leaked")
	}
}
func TestCatalogIntegrity(t *testing.T) {
	if len(Default.Entries) < 390 {
		t.Fatal("incomplete catalog")
	}
	images := 0
	for _, e := range Default.Entries {
		if e.ID == "" || e.Name == "" {
			t.Fatal("missing identity")
		}
		images += len(e.Illustrations)
		if e.Markdown == "" && len(e.Illustrations) == 0 {
			t.Fatal("empty content")
		}
	}
	if images != 341 {
		t.Fatalf("images: %d", images)
	}
}

func TestCategoryAndFavoriteContent(t *testing.T) {
	entries := []Entry{
		{ID: "a", Name: "A", Kind: "recipe", Category: "meat", DifficultyRank: 5, Illustrations: []string{"image.jpg"}},
		{ID: "b", Name: "B", Kind: "recipe", Category: "meat", DifficultyRank: 1},
		{ID: "c", Name: "C", Kind: "recipe", Category: "vegetable"},
	}
	c := &Catalog{Entries: entries, byID: map[string]Entry{}}
	for _, e := range entries {
		c.byID[e.ID] = e
	}
	got := c.Search(Filter{Kind: "recipe", Category: "meat"}, nil)
	if got.Total != 2 || got.Items[0].ID != "a" || got.Items[1].ID != "b" {
		t.Fatalf("category/default order: %+v", got)
	}
	if len(got.Items[0].Illustrations) != 1 || len(got.Items[1].Illustrations) != 0 {
		t.Fatal("both illustrated and text recipes must be retained")
	}
	favorites := c.Search(Filter{Kind: "recipe", Favorites: true}, map[string]bool{"b": true, "missing": true})
	if favorites.Total != 2 {
		t.Fatalf("favorite or unavailable entry missing: %+v", favorites)
	}
	for _, e := range favorites.Items {
		if !e.Favorite || (e.ID != "b" && e.ID != "missing") {
			t.Fatalf("incorrect favorite: %+v", e)
		}
	}
}
