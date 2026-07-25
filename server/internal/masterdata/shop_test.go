package masterdata

import (
	"testing"

	"lunar-tear/server/internal/store"
)

// tiers mirrors the shape m_shop_replaceable_gem has after the loader sorts it:
// first reroll of the day free, then an escalating gem price.
func tiers() [][2]int32 {
	return [][2]int32{{1, 0}, {2, 20}, {3, 40}, {4, 80}}
}

func TestRefreshGemCost(t *testing.T) {
	cat := &ShopCatalog{ReplaceableGem: tiers()}

	tests := []struct {
		name  string
		count int32
		want  int32
	}{
		{"first reroll is free", 1, 0},
		{"second costs the tier-2 price", 2, 20},
		{"third escalates again", 3, 40},
		{"fourth hits the top tier", 4, 80},
		{"beyond the last tier stays at the top price", 9, 80},
		{"below the first tier costs nothing", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cat.RefreshGemCost(tt.count); got != tt.want {
				t.Errorf("RefreshGemCost(%d) = %d, want %d", tt.count, got, tt.want)
			}
		})
	}
}

func TestRefreshGemCostNoTiers(t *testing.T) {
	// A master-data bundle without the table must not start charging arbitrary
	// amounts — absent pricing means free.
	cat := &ShopCatalog{}
	if got := cat.RefreshGemCost(5); got != 0 {
		t.Errorf("RefreshGemCost with no tiers = %d, want 0", got)
	}
}

func TestApplyShopDailyRestock(t *testing.T) {
	const now int64 = 1785000000000
	pool := []int32{10, 20}

	user := store.UserState{
		ShopReplaceable: store.UserShopReplaceableState{
			LineupUpdateCount:          3,
			LatestLineupUpdateDatetime: 1,
		},
		ShopItems: map[int32]store.UserShopItemState{
			10: {ShopItemId: 10, BoughtCount: 2, LatestVersion: 1},
			20: {ShopItemId: 20, BoughtCount: 5, LatestVersion: 1},
			99: {ShopItemId: 99, BoughtCount: 7, LatestVersion: 1}, // not in the pool
		},
	}

	ApplyShopDailyRestock(&user, pool, now)

	if user.ShopReplaceable.LineupUpdateCount != 0 {
		t.Errorf("LineupUpdateCount = %d, want 0", user.ShopReplaceable.LineupUpdateCount)
	}
	if user.ShopReplaceable.LatestLineupUpdateDatetime != now {
		t.Errorf("LatestLineupUpdateDatetime = %d, want %d", user.ShopReplaceable.LatestLineupUpdateDatetime, now)
	}
	for _, id := range pool {
		if got := user.ShopItems[id].BoughtCount; got != 0 {
			t.Errorf("pool item %d BoughtCount = %d, want 0", id, got)
		}
		if got := user.ShopItems[id].LatestVersion; got != now {
			t.Errorf("pool item %d LatestVersion = %d, want %d", id, got, now)
		}
	}
	if got := user.ShopItems[99].BoughtCount; got != 7 {
		t.Errorf("non-pool item BoughtCount = %d, want 7 (must be untouched)", got)
	}
}

// The two features interlock: the daily restock is what re-arms the free tier,
// otherwise the refresh price would escalate forever.
func TestDailyRestockReArmsTheFreeRefresh(t *testing.T) {
	cat := &ShopCatalog{ReplaceableGem: tiers()}
	user := store.UserState{
		ShopReplaceable: store.UserShopReplaceableState{LineupUpdateCount: 3},
		ShopItems:       map[int32]store.UserShopItemState{},
	}

	if cost := cat.RefreshGemCost(user.ShopReplaceable.LineupUpdateCount + 1); cost != 80 {
		t.Fatalf("next refresh before restock = %d gems, want 80", cost)
	}

	ApplyShopDailyRestock(&user, nil, 1785000000000)

	if cost := cat.RefreshGemCost(user.ShopReplaceable.LineupUpdateCount + 1); cost != 0 {
		t.Errorf("next refresh after restock = %d gems, want 0", cost)
	}
}
