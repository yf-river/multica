package meals

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrConflict = errors.New("饮食内容已变化，请刷新后重试；本次输入已保留")

type Preferences struct {
	Goal         string `json:"goal"`
	Activity     string `json:"activity"`
	Restrictions string `json:"restrictions"`
	People       int    `json:"people"`
	PrepTime     string `json:"prep_time"`
	Notes        string `json:"notes"`
}
type Food struct {
	Name     string   `json:"name"`
	Quantity *float64 `json:"quantity"`
	Unit     string   `json:"unit"`
	Basis    string   `json:"basis"`
	Origin   string   `json:"origin"`
	Original string   `json:"original"`
}
type Entry struct {
	ID       string `json:"id"`
	Date     string `json:"date"`
	Slot     string `json:"slot"`
	Name     string `json:"name"`
	Portions int    `json:"portions"`
	Mode     string `json:"mode"`
	RecipeID string `json:"recipe_id"`
	BatchID  string `json:"batch_id"`
	Foods    []Food `json:"foods"`
	Note     string `json:"note"`
}
type Source struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Original string `json:"original"`
}
type Ingredient struct {
	GroupKey string `json:"group_key"`
	ID       string `json:"id"`
	Week     string `json:"week"`
	Food
	Status  string   `json:"status"`
	Manual  bool     `json:"manual"`
	Sources []Source `json:"sources"`
	Stale   bool     `json:"stale"`
}
type Prep struct {
	Stale    bool     `json:"stale"`
	ID       string   `json:"id"`
	Week     string   `json:"week"`
	Name     string   `json:"name"`
	Portions int      `json:"portions"`
	RecipeID string   `json:"recipe_id"`
	Steps    string   `json:"steps"`
	EntryIDs []string `json:"entry_ids"`
}
type Use struct {
	ID       string `json:"id"`
	Date     string `json:"date"`
	Portions int    `json:"portions"`
}
type Batch struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Portions     int      `json:"portions"`
	MadeAt       string   `json:"made_at"`
	Storage      string   `json:"storage"`
	PlannedDates []string `json:"planned_dates"`
	RecipeID     string   `json:"recipe_id"`
	PlanID       string   `json:"plan_id"`
	Uses         []Use    `json:"uses"`
}

func (b Batch) Remaining() int {
	n := b.Portions
	for _, u := range b.Uses {
		n -= u.Portions
	}
	return n
}

type Draft struct {
	Preferences    *Preferences `json:"preferences,omitempty"`
	Version        int64        `json:"version"`
	BaseEntries    []Entry      `json:"base_entries"`
	ID             string       `json:"id"`
	Week           string       `json:"week"`
	HealthRevision int64        `json:"health_revision"`
	Rationale      string       `json:"rationale"`
	Entries        []Entry      `json:"entries"`
	PrepSteps      string       `json:"prep_steps"`
	Status         string       `json:"status"`
}
type TaskLink struct {
	Key     string `json:"key"`
	IssueID string `json:"issue_id"`
}
type Receipt struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}
type State struct {
	Preferences Preferences  `json:"preferences"`
	Entries     []Entry      `json:"entries"`
	Shopping    []Ingredient `json:"shopping"`
	Plans       []Prep       `json:"plans"`
	Batches     []Batch      `json:"batches"`
	Drafts      []Draft      `json:"drafts"`
	Tasks       []TaskLink   `json:"tasks"`
	Receipts    []Receipt    `json:"receipts,omitempty"`
}

func (s *State) Init() {
	if s.Preferences.People == 0 {
		s.Preferences.People = 1
	}
	if s.Entries == nil {
		s.Entries = []Entry{}
	}
	if s.Shopping == nil {
		s.Shopping = []Ingredient{}
	}
	if s.Plans == nil {
		s.Plans = []Prep{}
	}
	if s.Batches == nil {
		s.Batches = []Batch{}
	}
	if s.Drafts == nil {
		s.Drafts = []Draft{}
	}
	if s.Tasks == nil {
		s.Tasks = []TaskLink{}
	}
	for i := range s.Entries {
		if s.Entries[i].Foods == nil {
			s.Entries[i].Foods = []Food{}
		}
	}
	for i := range s.Drafts {
		if s.Drafts[i].Entries == nil {
			s.Drafts[i].Entries = []Entry{}
		}
		for j := range s.Drafts[i].Entries {
			if s.Drafts[i].Entries[j].Foods == nil {
				s.Drafts[i].Entries[j].Foods = []Food{}
			}
		}
	}
}

type Command struct {
	DraftVersion int64       `json:"draft_version"`
	Action       string      `json:"action"`
	Revision     int64       `json:"revision"`
	RequestID    string      `json:"request_id"`
	ID           string      `json:"id"`
	Week         string      `json:"week"`
	Entry        Entry       `json:"entry"`
	Ingredient   Ingredient  `json:"ingredient"`
	Batch        Batch       `json:"batch"`
	Use          Use         `json:"use"`
	Draft        Draft       `json:"draft"`
	Preferences  Preferences `json:"preferences"`
	EntryIDs     []string    `json:"entry_ids"`
	IssueID      string      `json:"issue_id"`
}

func (c Command) Digest() string {
	c.Revision = 0
	c.RequestID = ""
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *State) Replayed(c Command) (bool, error) {
	for _, r := range s.Receipts {
		if r.ID == c.RequestID {
			if r.Digest != c.Digest() {
				return false, errors.New("这次操作标识已用于其他内容")
			}
			return true, nil
		}
	}
	return false, nil
}
func (s *State) Remember(c Command) {
	s.Receipts = append(s.Receipts, Receipt{c.RequestID, c.Digest()})
	if len(s.Receipts) > 2048 {
		s.Receipts = s.Receipts[len(s.Receipts)-2048:]
	}
}
func ID() string { return uuid.NewString() }
func ValidDate(s string) bool {
	t, e := time.Parse("2006-01-02", s)
	return e == nil && t.Format("2006-01-02") == s
}
func Week(date string) string {
	t, e := time.Parse("2006-01-02", date)
	if e != nil {
		return ""
	}
	offset := (int(t.Weekday()) + 6) % 7
	return t.AddDate(0, 0, -offset).Format("2006-01-02")
}
func validID(s string) bool       { _, e := uuid.Parse(s); return e == nil }
func text(s string, max int) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= max }
func ValidateFood(f Food) error {
	if !text(f.Name, 200) || len(f.Unit) > 40 || len(f.Basis) > 100 || len(f.Original) > 3000 {
		return errors.New("请填写食材名称，名称最多 200 字")
	}
	if f.Quantity != nil && (!finite(*f.Quantity) || *f.Quantity <= 0 || *f.Quantity > 1000000) {
		return errors.New("食材数量须为大于零的有效数字")
	}
	switch f.Origin {
	case "recipe", "assistant", "manual":
	default:
		return errors.New("请选择食材用量来源")
	}
	return nil
}
func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func ValidateEntry(e Entry) error {
	if !ValidDate(e.Date) || !text(e.Name, 200) || e.Portions < 1 || e.Portions > 1000 {
		return errors.New("请填写日期、餐食名称及 1—1000 份的份数")
	}
	switch e.Slot {
	case "breakfast", "lunch", "dinner", "snack":
	default:
		return errors.New("请选择餐次")
	}
	switch e.Mode {
	case "home", "out", "delivery":
	default:
		return errors.New("请选择用餐方式")
	}
	if e.BatchID != "" && !validID(e.BatchID) {
		return errors.New("批次标识无效")
	}
	if len(e.Foods) > 100 || len(e.Note) > 5000 {
		return errors.New("餐食内容过长")
	}
	for _, f := range e.Foods {
		if err := ValidateFood(f); err != nil {
			return err
		}
	}
	return nil
}
func (s *State) Apply(c Command) error {
	switch c.Action {
	case "preferences":
		p := c.Preferences
		if p.People < 1 || p.People > 100 || len(p.Goal)+len(p.Activity)+len(p.Restrictions)+len(p.Notes)+len(p.PrepTime) > 12000 {
			return errors.New("请填写有效的用餐人数和简短偏好")
		}
		s.Preferences = p
	case "entry_save":
		e := c.Entry
		if err := ValidateEntry(e); err != nil {
			return err
		}
		if e.BatchID != "" {
			found := false
			for _, b := range s.Batches {
				if b.ID == e.BatchID {
					found = true
				}
			}
			if !found {
				return errors.New("关联批次不存在")
			}
		}
		if e.ID == "" {
			e.ID = ID()
			s.Entries = append(s.Entries, e)
		} else {
			found := false
			for i, x := range s.Entries {
				if x.ID == e.ID {
					s.Entries[i] = e
					found = true
				}
			}
			if !found {
				return errors.New("餐食已移除，请刷新")
			}
		}
	case "entry_remove":
		found := false
		for i, e := range s.Entries {
			if e.ID == c.ID {
				s.Entries = append(s.Entries[:i], s.Entries[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return errors.New("餐食不存在")
		}
	case "shopping_generate":
		if !ValidDate(c.Week) || Week(c.Week) != c.Week {
			return errors.New("请选择周起始日")
		}
		s.GenerateShopping(c.Week, c.EntryIDs)
		s.GeneratePlans(c.Week, "")
	case "ingredient_save":
		f := c.Ingredient
		if err := ValidateFood(f.Food); err != nil {
			return err
		}
		if !ValidDate(f.Week) {
			return errors.New("采购周无效")
		}
		switch f.Status {
		case "needed", "available", "purchased":
		default:
			return errors.New("采购状态无效")
		}
		f.Manual = true
		if f.ID == "" {
			f.ID = ID()
			f.Sources = []Source{}
			s.Shopping = append(s.Shopping, f)
		} else {
			found := false
			for i, x := range s.Shopping {
				if x.ID == f.ID {
					f.GroupKey = x.GroupKey
					f.Sources = x.Sources
					f.Stale = x.Stale
					s.Shopping[i] = f
					found = true
				}
			}
			if !found {
				return errors.New("食材已移除")
			}
		}
	case "ingredient_status":
		switch c.Ingredient.Status {
		case "needed", "available", "purchased":
		default:
			return errors.New("采购状态无效")
		}
		found := false
		for i, f := range s.Shopping {
			if f.ID == c.ID {
				s.Shopping[i].Status = c.Ingredient.Status
				found = true
			}
		}
		if !found {
			return errors.New("食材不存在")
		}
	case "ingredient_remove":
		found := false
		for i, f := range s.Shopping {
			if f.ID == c.ID {
				s.Shopping = append(s.Shopping[:i], s.Shopping[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return errors.New("食材不存在")
		}
	case "batch_save":
		b := c.Batch
		if !text(b.Name, 200) || b.Portions < 1 || b.Portions > 1000 || !ValidDate(b.MadeAt) {
			return errors.New("请填写批次名称、实际份数和制作日期")
		}
		switch b.Storage {
		case "frozen", "refrigerated", "room":
		default:
			return errors.New("请选择保存方式")
		}
		for _, d := range b.PlannedDates {
			if !ValidDate(d) {
				return errors.New("计划食用日期无效")
			}
		}
		if b.PlanID != "" {
			found := false
			for _, p := range s.Plans {
				if p.ID == b.PlanID {
					found = true
				}
			}
			if !found {
				return errors.New("备餐计划不存在")
			}
		}
		if b.ID == "" {
			b.ID = ID()
			b.Uses = []Use{}
			s.Batches = append(s.Batches, b)
		} else {
			found := false
			for i, x := range s.Batches {
				if x.ID == b.ID {
					b.Uses = x.Uses
					if b.Remaining() < 0 {
						return errors.New("总份数不能小于已取用份数")
					}
					s.Batches[i] = b
					found = true
				}
			}
			if !found {
				return errors.New("批次不存在")
			}
		}
	case "batch_use":
		if c.Use.Portions < 1 || !ValidDate(c.Use.Date) {
			return errors.New("请填写实际取用日期和份数")
		}
		found := false
		for i, b := range s.Batches {
			if b.ID == c.ID {
				if c.Use.Portions > b.Remaining() {
					return errors.New("取用份数超过剩余份数，请调整")
				}
				u := c.Use
				u.ID = c.RequestID
				s.Batches[i].Uses = append(b.Uses, u)
				found = true
			}
		}
		if !found {
			return errors.New("批次不存在")
		}
	case "draft_start":
		if !ValidDate(c.Week) || Week(c.Week) != c.Week {
			return errors.New("请选择周起始日")
		}
		base := []Entry{}
		for _, e := range s.Entries {
			if Week(e.Date) == c.Week {
				base = append(base, e)
			}
		}
		s.Drafts = append(s.Drafts, Draft{ID: ID(), Week: c.Week, Status: "discussing", Entries: append([]Entry{}, base...), BaseEntries: base})
	case "draft_save":
		d := c.Draft
		if d.Preferences != nil {
			p := d.Preferences
			if p.People < 1 || p.People > 100 || len(p.Goal)+len(p.Activity)+len(p.Restrictions)+len(p.Notes)+len(p.PrepTime) > 12000 {
				return errors.New("饮食偏好无效")
			}
		}
		if !ValidDate(d.Week) || len(d.Entries) > 100 || len(d.Rationale) > 15000 || len(d.PrepSteps) > 15000 {
			return errors.New("菜单草稿格式无效")
		}
		for i := range d.Entries {
			if d.Entries[i].BatchID != "" {
				found := false
				for _, b := range s.Batches {
					if b.ID == d.Entries[i].BatchID {
						found = true
					}
				}
				if !found {
					return errors.New("关联批次不存在")
				}
			}
			if err := ValidateEntry(d.Entries[i]); err != nil {
				return err
			}
			if Week(d.Entries[i].Date) != d.Week {
				return errors.New("餐食日期不属于草稿所在周")
			}
			if d.Entries[i].ID == "" {
				d.Entries[i].ID = ID()
			}
		}
		found := false
		for i, x := range s.Drafts {
			if x.ID == d.ID {
				if x.Status == "confirmed" {
					return errors.New("这份菜单已确认，请新建调整草稿")
				}
				d.BaseEntries = x.BaseEntries
				d.Version = x.Version + 1
				d.Status = "proposed"
				s.Drafts[i] = d
				found = true
			}
		}
		if !found {
			return errors.New("草稿不存在")
		}
	case "draft_confirm":
		found := false
		for i, d := range s.Drafts {
			if d.ID == c.ID {
				found = true
				if d.Status == "confirmed" {
					return nil
				}
				if d.Version != c.DraftVersion {
					return ErrConflict
				}
				if d.Status != "proposed" || len(d.Entries) == 0 {
					return errors.New("请先完善菜单建议")
				}
				current := []Entry{}
				for _, e := range s.Entries {
					if Week(e.Date) == d.Week {
						current = append(current, e)
					}
				}
				expected, _ := json.Marshal(d.BaseEntries)
				actual, _ := json.Marshal(current)
				if string(expected) != string(actual) {
					return ErrConflict
				}
				keep := []Entry{}
				for _, e := range s.Entries {
					if Week(e.Date) != d.Week {
						keep = append(keep, e)
					}
				}
				s.Entries = keep
				for _, e := range d.Entries {
					exists := false
					for j, x := range s.Entries {
						if x.ID == e.ID {
							s.Entries[j] = e
							exists = true
						}
					}
					if !exists {
						s.Entries = append(s.Entries, e)
					}
				}
				if d.Preferences != nil {
					s.Preferences = *d.Preferences
				}
				s.GenerateShopping(d.Week, nil)
				s.GeneratePlans(d.Week, d.PrepSteps)
				s.Drafts[i].Status = "confirmed"
			}
		}
		if !found {
			return errors.New("草稿不存在")
		}
	default:
		return fmt.Errorf("不支持的饮食操作")
	}
	if c.Action == "entry_save" || c.Action == "entry_remove" {
		id := c.ID
		if c.Action == "entry_save" {
			id = c.Entry.ID
		}
		if id != "" {
			for i := range s.Plans {
				for _, entryID := range s.Plans[i].EntryIDs {
					if entryID == id {
						s.Plans[i].Stale = true
					}
				}
			}
			for i := range s.Shopping {
				for _, source := range s.Shopping[i].Sources {
					if strings.HasPrefix(source.Key, id+":") {
						s.Shopping[i].Stale = true
						break
					}
				}
			}
		}
	}
	return nil
}
