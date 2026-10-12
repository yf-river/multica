package meals

import "testing"

func amount(n float64) *float64 { return &n }
func meal(date string, foods ...Food) Entry {
	return Entry{ID: ID(), Date: date, Slot: "lunch", Name: "集中备餐", Portions: 2, Mode: "home", Foods: foods}
}
func TestMealShoppingPreservesManualAndPurchase(t *testing.T) {
	s := State{}
	s.Init()
	f := Food{Name: "鸡蛋", Quantity: amount(2), Unit: "个", Basis: "个数", Origin: "manual"}
	s.Entries = []Entry{meal("2026-10-12", f), meal("2026-10-13", f)}
	s.GenerateShopping("2026-10-12", nil)
	if len(s.Shopping) != 1 || *s.Shopping[0].Quantity != 8 {
		t.Fatalf("same unit aggregate: %+v", s.Shopping)
	}
	id := s.Shopping[0].ID
	s.GenerateShopping("2026-10-12", nil)
	if len(s.Shopping) != 1 || s.Shopping[0].ID != id || *s.Shopping[0].Quantity != 8 {
		t.Fatal("repeat generation duplicated quantities")
	}
	s.Shopping[0].Name = "用户选择的替代食材"
	s.Shopping[0].Manual = true
	s.Shopping[0].Quantity = amount(10)
	s.Shopping[0].Status = "purchased"
	s.Entries[0].Portions = 3
	s.GenerateShopping("2026-10-12", nil)
	if s.Shopping[0].Name != "用户选择的替代食材" || *s.Shopping[0].Quantity != 10 || s.Shopping[0].Status != "purchased" {
		t.Fatal("generation replaced user decision")
	}
	s.Entries = nil
	s.GenerateShopping("2026-10-12", nil)
	if len(s.Shopping) != 1 || !s.Shopping[0].Stale {
		t.Fatal("removed meals destroyed confirmed shopping")
	}
}
func TestMealUnknownUnitsAndBatchDoNotInventPurchase(t *testing.T) {
	s := State{}
	s.Init()
	s.Entries = []Entry{meal("2026-10-12", Food{Name: "鸡肉", Origin: "recipe"}), meal("2026-10-13", Food{Name: "鸡肉", Origin: "recipe"}), meal("2026-10-14", Food{Name: "鸡肉", Quantity: amount(100), Unit: "克", Basis: "生重", Origin: "assistant"}), meal("2026-10-15", Food{Name: "鸡肉", Quantity: amount(1), Unit: "千克", Basis: "生重", Origin: "manual"})}
	s.Entries[3].BatchID = ID()
	s.GenerateShopping("2026-10-12", nil)
	if len(s.Shopping) != 3 {
		t.Fatalf("unknown quantities or existing batch miscounted %d", len(s.Shopping))
	}
	for _, f := range s.Shopping {
		if f.Unit == "千克" {
			t.Fatal("existing batch generated purchases")
		}
	}
}
func TestMealDraftConfirmationAndUse(t *testing.T) {
	s := State{}
	s.Init()
	start := Command{Action: "draft_start", Week: "2026-10-12"}
	if err := s.Apply(start); err != nil {
		t.Fatal(err)
	}
	d := s.Drafts[0]
	d.Entries = []Entry{meal("2026-10-12", Food{Name: "米", Quantity: amount(80), Unit: "克", Basis: "生重", Origin: "assistant"})}
	d.PrepSteps = "按菜谱集中制作"
	if err := s.Apply(Command{Action: "draft_save", Draft: d}); err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 0 || len(s.Plans) != 0 {
		t.Fatal("proposal became an executed plan")
	}
	confirm := Command{Action: "draft_confirm", ID: d.ID, DraftVersion: 1}
	if err := s.Apply(confirm); err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 1 || len(s.Plans) != 1 || len(s.Batches) != 0 {
		t.Fatal("confirmation did not separate plans from inventory")
	}
	if err := s.Apply(confirm); err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 1 {
		t.Fatal("confirmation duplicated meals")
	}
	if err := s.Apply(Command{Action: "batch_save", Batch: Batch{Name: "米饭", MadeAt: "2026-10-11", Storage: "frozen", Portions: 3}}); err != nil {
		t.Fatal(err)
	}
	id := s.Batches[0].ID
	use := Command{Action: "batch_use", RequestID: ID(), ID: id, Use: Use{Date: "2026-10-12", Portions: 2}}
	if err := s.Apply(use); err != nil {
		t.Fatal(err)
	}
	s.Remember(use)
	ok, err := s.Replayed(use)
	if !ok || err != nil {
		t.Fatal("retry receipt lost")
	}
	if s.Batches[0].Remaining() != 1 {
		t.Fatal("incorrect remaining")
	}
	if err := s.Apply(Command{Action: "batch_use", ID: id, Use: Use{Date: "2026-10-12", Portions: 2}}); err == nil {
		t.Fatal("allowed overdrawing inventory")
	}
	use.Use.Portions = 1
	if _, err := s.Replayed(use); err == nil {
		t.Fatal("same key accepted different request")
	}
}
func TestMealRecipeFoodsKeepAlternatives(t *testing.T) {
	foods := RecipeFoods("- 手枪腿（或者鸡胸脯肉）\n- 盐\n### 可选原料\n- 豆瓣酱")
	if len(foods) != 2 || foods[0].Name != "手枪腿（或者鸡胸脯肉）" || foods[0].Quantity != nil {
		t.Fatalf("invented ingredient quantity or alternative: %+v", foods)
	}
}
