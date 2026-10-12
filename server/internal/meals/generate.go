package meals

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 用量口径参与合并；无法比较的原文各自保留。
func foodKey(f Food, source string) string {
	if f.Quantity == nil || f.Unit == "" || f.Basis == "" {
		return "source:" + source
	}
	encoded, _ := json.Marshal([]string{f.Name, f.Unit, f.Basis})
	return string(encoded)
}
func (s *State) GenerateShopping(week string, selection []string) {
	desired := []Ingredient{}
	indices := map[string]int{}
	selected := map[string]bool{}
	for _, id := range selection {
		selected[id] = true
	}
	for _, e := range s.Entries {
		if Week(e.Date) != week || e.Mode != "home" || e.BatchID != "" || (len(selection) > 0 && !selected[e.ID]) {
			continue
		}
		for i, f := range e.Foods {
			source := fmt.Sprintf("%s:%d", e.ID, i)
			key := foodKey(f, source)
			quantity := f.Quantity
			if quantity != nil {
				n := *quantity * float64(e.Portions)
				quantity = &n
			}
			if at, ok := indices[key]; ok {
				n := *desired[at].Quantity + *quantity
				desired[at].Quantity = &n
				desired[at].Sources = append(desired[at].Sources, Source{source, e.Name, f.Original})
			} else {
				f.Quantity = quantity
				indices[key] = len(desired)
				desired = append(desired, Ingredient{ID: ID(), Week: week, GroupKey: key, Food: f, Status: "needed", Sources: []Source{{source, e.Name, f.Original}}})
			}
		}
	}
	used := map[int]bool{}
	for i := range desired {
		d := &desired[i]
		key := foodKey(d.Food, d.Sources[0].Key)
		for j, old := range s.Shopping {
			if old.Week != week || len(old.Sources) == 0 || used[j] {
				continue
			}
			oldKey := old.GroupKey
			if oldKey == "" {
				oldKey = foodKey(old.Food, old.Sources[0].Key)
			}
			if oldKey == key {
				d.ID = old.ID
				d.Status = old.Status
				d.Manual = old.Manual
				if old.Manual {
					d.Name = old.Name
					d.Original = old.Original
					d.Quantity = old.Quantity
					d.Unit = old.Unit
					d.Basis = old.Basis
					d.Origin = old.Origin
				}
				used[j] = true
				break
			}
		}
	}
	retained := []Ingredient{}
	for i, old := range s.Shopping {
		if old.Week != week || len(old.Sources) == 0 {
			retained = append(retained, old)
		} else if !used[i] {
			old.Stale = true
			retained = append(retained, old)
		}
	}
	s.Shopping = append(retained, desired...)
}
func (s *State) GeneratePlans(week, steps string) {
	groups := map[string]int{}
	plans := []Prep{}
	for _, e := range s.Entries {
		if Week(e.Date) != week || e.Mode != "home" || e.BatchID != "" {
			continue
		}
		key := e.RecipeID
		if key == "" {
			key = e.Name
		}
		if at, ok := groups[key]; ok {
			plans[at].Portions += e.Portions
			plans[at].EntryIDs = append(plans[at].EntryIDs, e.ID)
		} else {
			groups[key] = len(plans)
			plans = append(plans, Prep{ID: ID(), Week: week, Name: e.Name, RecipeID: e.RecipeID, Portions: e.Portions, EntryIDs: []string{e.ID}, Steps: steps})
		}
	}
	for i := range plans {
		for _, old := range s.Plans {
			if old.Week == week && ((old.RecipeID != "" && old.RecipeID == plans[i].RecipeID) || (old.RecipeID == "" && old.Name == plans[i].Name)) {
				plans[i].ID = old.ID
				break
			}
		}
	}
	kept := []Prep{}
	for _, p := range s.Plans {
		if p.Week != week {
			kept = append(kept, p)
			continue
		}
		present := false
		for _, next := range plans {
			if next.ID == p.ID {
				present = true
			}
		}
		referenced := false
		for _, b := range s.Batches {
			if b.PlanID == p.ID {
				referenced = true
			}
		}
		for _, l := range s.Tasks {
			if l.Key == "prep:"+p.ID {
				referenced = true
			}
		}
		if !present && referenced {
			p.Stale = true
			kept = append(kept, p)
		}
	}
	s.Plans = append(kept, plans...)
}

// Markdown 标题决定可选范围，替代原料和无数量的条目按原文保存。
func RecipeFoods(raw string) []Food {
	out := []Food{}
	optional := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			optional = strings.Contains(line, "可选")
			continue
		}
		if optional || strings.Contains(line, "可选") {
			continue
		}
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			name := strings.TrimSpace(line[2:])
			if name != "" {
				out = append(out, Food{Name: name, Origin: "recipe", Original: name})
			}
		}
	}
	return out
}
