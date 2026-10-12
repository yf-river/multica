// Package cooking serves the immutable, release-bound cooking catalog.
package cooking

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed catalog.json
var files embed.FS

type Entry struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Category       string   `json:"category"`
	CategoryName   string   `json:"category_name"`
	Markdown       string   `json:"markdown,omitempty"`
	Difficulty     string   `json:"difficulty"`
	DifficultyRank int      `json:"difficulty_rank"`
	Ingredients    string   `json:"ingredients,omitempty"`
	Cover          string   `json:"cover"`
	Illustrations  []string `json:"illustrations"`
	SourcePath     string   `json:"source_path,omitempty"`
	SourceURL      string   `json:"source_url,omitempty"`
	ImageSourceURL string   `json:"image_source_url,omitempty"`
	Available      bool     `json:"available"`
	Favorite       bool     `json:"favorite"`
}
type Catalog struct {
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
	byID    map[string]Entry
}

var Default = mustLoad()

func mustLoad() *Catalog {
	data, err := files.ReadFile("catalog.json")
	if err != nil {
		panic(err)
	}
	var c Catalog
	if err = json.Unmarshal(data, &c); err != nil {
		panic(err)
	}
	c.byID = make(map[string]Entry, len(c.Entries))
	for _, e := range c.Entries {
		if _, ok := c.byID[e.ID]; ok {
			panic(fmt.Sprintf("duplicate cooking ID %s", e.ID))
		}
		c.byID[e.ID] = e
	}
	return &c
}
func (c *Catalog) Get(id string) (Entry, bool) { e, ok := c.byID[id]; return e, ok }

type Filter struct {
	Kind, Query, Category string
	Favorites             bool
	Page                  int
}
type List struct {
	Items    []Entry `json:"items"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Version  string  `json:"version"`
}

func card(e Entry) Entry { e.Markdown = ""; e.Ingredients = ""; return e }
func rank(e Entry, q string) int {
	if q == "" {
		return 0
	}
	if strings.Contains(strings.ToLower(e.Name), q) {
		return 3
	}
	if strings.Contains(strings.ToLower(e.Ingredients), q) {
		return 2
	}
	if e.Kind != "tip" && strings.Contains(strings.ToLower(e.Markdown), q) {
		return 1
	}
	return -1
}
func (c *Catalog) Search(f Filter, favorites map[string]bool) List {
	items := []Entry{}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	for _, e := range c.Entries {
		if e.Kind != f.Kind || f.Category != "" && e.Category != f.Category || f.Favorites && !favorites[e.ID] || rank(e, q) < 0 {
			continue
		}
		e.Favorite = favorites[e.ID]
		items = append(items, e)
	}
	if f.Favorites {
		for id := range favorites {
			if _, ok := c.Get(id); !ok && f.Category == "" {
				e := Entry{ID: id, Name: "该菜谱暂不可用", Kind: "recipe", Favorite: true, Illustrations: []string{}}
				if rank(e, q) >= 0 {
					items = append(items, e)
				}
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if q != "" && rank(a, q) != rank(b, q) {
			return rank(a, q) > rank(b, q)
		}

		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	if f.Page < 1 {
		f.Page = 1
	}
	total := len(items)
	start := (f.Page - 1) * 24
	if start > total || start < 0 {
		start = total
	}
	end := min(start+24, total)
	out := List{Items: []Entry{}, Total: total, Page: f.Page, PageSize: 24, Version: c.Version}
	for _, e := range items[start:end] {
		out.Items = append(out.Items, card(e))
	}
	return out
}

type Category struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (c *Catalog) Categories() []Category {
	result := []Category{}
	for _, e := range c.Entries {
		if e.Kind != "recipe" {
			continue
		}
		found := false
		for i := range result {
			if result[i].ID == e.Category {
				result[i].Count++
				found = true
				break
			}
		}
		if !found {
			result = append(result, Category{e.Category, e.CategoryName, 1})
		}
	}
	return result
}
