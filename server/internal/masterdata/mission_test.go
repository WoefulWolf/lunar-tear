package masterdata

import (
	"testing"
	"time"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/timeline"
)

func TestEvaluateMissionCtx(t *testing.T) {
	inProgress := int32(model.MissionProgressStatusTypeInProgress)
	clear := int32(model.MissionProgressStatusTypeClear)

	tests := []struct {
		name         string
		ctx          MissionEvalContext
		def          MissionDef
		wantProgress int32
		wantStatus   int32
	}{
		{
			name:         "daily summon, partway",
			ctx:          MissionEvalContext{SummonsSinceDailyReset: 1},
			def:          MissionDef{ClearConditionType: ClearConditionPerformSummon, ClearConditionValue: 3, SubCategoryId: dailySubCategory},
			wantProgress: 1,
			wantStatus:   inProgress,
		},
		{
			name:         "daily summon, complete",
			ctx:          MissionEvalContext{SummonsSinceDailyReset: 3},
			def:          MissionDef{ClearConditionType: ClearConditionPerformSummon, ClearConditionValue: 3, SubCategoryId: dailySubCategory},
			wantProgress: 3,
			wantStatus:   clear,
		},
		{
			name:         "favorite costume set",
			ctx:          MissionEvalContext{FavoriteCostumeSet: true},
			def:          MissionDef{ClearConditionType: ClearConditionSetFavCostume, ClearConditionValue: 1},
			wantProgress: 1,
			wantStatus:   clear,
		},
		{
			name:         "unmapped condition type stays in progress",
			ctx:          MissionEvalContext{},
			def:          MissionDef{ClearConditionType: 999, ClearConditionValue: 1},
			wantProgress: 0,
			wantStatus:   inProgress,
		},
		{
			name:         "quest clear, whitelisted group, partway",
			ctx:          MissionEvalContext{ClearsSinceDailyReset: 2},
			def:          MissionDef{ClearConditionType: ClearConditionClearQuest, ClearConditionGroupId: 9, ClearConditionValue: 5, SubCategoryId: dailySubCategory},
			wantProgress: 2,
			wantStatus:   inProgress,
		},
		{
			name:         "quest clear, non-whitelisted group is ignored",
			ctx:          MissionEvalContext{ClearsSinceDailyReset: 99},
			def:          MissionDef{ClearConditionType: ClearConditionClearQuest, ClearConditionGroupId: 1234, ClearConditionValue: 5, SubCategoryId: dailySubCategory},
			wantProgress: 0,
			wantStatus:   inProgress,
		},
		{
			name:         "quest clear, whitelisted but constrained by optGroup",
			ctx:          MissionEvalContext{ClearsSinceDailyReset: 99},
			def:          MissionDef{ClearConditionType: ClearConditionClearQuest, ClearConditionGroupId: 9, ClearConditionOptionGroupId: 7, ClearConditionValue: 5, SubCategoryId: dailySubCategory},
			wantProgress: 0,
			wantStatus:   inProgress,
		},
		{
			name:         "daily purchase uses day-scoped counter",
			ctx:          MissionEvalContext{PurchasesSinceDailyReset: 1, ShopItemsBought: 50},
			def:          MissionDef{ClearConditionType: ClearConditionPurchaseItem, ClearConditionValue: 1, SubCategoryId: dailySubCategory},
			wantProgress: 1,
			wantStatus:   clear,
		},
		{
			name:         "non-daily purchase uses lifetime bought count",
			ctx:          MissionEvalContext{PurchasesSinceDailyReset: 0, ShopItemsBought: 10},
			def:          MissionDef{ClearConditionType: ClearConditionPurchaseItem, ClearConditionValue: 10, SubCategoryId: 2},
			wantProgress: 10,
			wantStatus:   clear,
		},
		{
			name:         "daily login auto-satisfies",
			ctx:          MissionEvalContext{},
			def:          MissionDef{ClearConditionType: ClearConditionLogin, ClearConditionValue: 1, SubCategoryId: dailySubCategory},
			wantProgress: 1,
			wantStatus:   clear,
		},
		{
			name:         "non-daily login does not auto-satisfy",
			ctx:          MissionEvalContext{},
			def:          MissionDef{ClearConditionType: ClearConditionLogin, ClearConditionValue: 1, SubCategoryId: 2},
			wantProgress: 0,
			wantStatus:   inProgress,
		},
		{
			name:         "zero condition value clamps target to 1",
			ctx:          MissionEvalContext{},
			def:          MissionDef{ClearConditionType: ClearConditionLogin, ClearConditionValue: 0, SubCategoryId: dailySubCategory},
			wantProgress: 1,
			wantStatus:   clear,
		},
		{
			name:         "favorite costume not set",
			ctx:          MissionEvalContext{FavoriteCostumeSet: false},
			def:          MissionDef{ClearConditionType: ClearConditionSetFavCostume, ClearConditionValue: 1},
			wantProgress: 0,
			wantStatus:   inProgress,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProgress, gotStatus := EvaluateMissionCtx(tt.ctx, tt.def)
			if gotProgress != tt.wantProgress || gotStatus != tt.wantStatus {
				t.Errorf("got (progress=%d, status=%d), want (progress=%d, status=%d)",
					gotProgress, gotStatus, tt.wantProgress, tt.wantStatus)
			}
		})
	}
}

func TestTermActive(t *testing.T) {
	cat := MissionCatalog{Terms: map[int32]MissionTerm{7: {Start: 1000, End: 2000}}}

	tests := []struct {
		name   string
		termId int32
		now    int64
		want   bool
	}{
		{"before window", 7, 999, false},
		{"at start is inclusive", 7, 1000, true},
		{"inside window", 7, 1500, true},
		{"at end is exclusive", 7, 2000, false},
		{"after window", 7, 2001, false},
		{"unknown term is always active", 99, 1500, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cat.TermActive(tt.termId, timeline.ContentMillis(tt.now)); got != tt.want {
				t.Errorf("TermActive(%d, %d) = %v, want %v", tt.termId, tt.now, got, tt.want)
			}
		})
	}
}

func TestBuildMissionEvalContext(t *testing.T) {
	user := store.UserState{
		Quests: map[int32]store.UserQuestState{
			101: {ClearCount: 6},
			102: {ClearCount: 4},
		},
		ShopItems: map[int32]store.UserShopItemState{
			5: {BoughtCount: 2},
			6: {BoughtCount: 3},
		},
		LifetimeCounters: store.LifetimeCountersState{SummonCount: 9, PurchaseCount: 6},
		DailyMission: store.DailyMissionState{
			ClearBaseline:    4,
			SummonBaseline:   3,
			PurchaseBaseline: 6,
		},
		Profile: store.UserProfileState{FavoriteCostumeId: 100},
	}

	ctx := BuildMissionEvalContext(user)

	if ctx.ClearsSinceDailyReset != 6 { // (6+4) - 4
		t.Errorf("ClearsSinceDailyReset = %d, want 6", ctx.ClearsSinceDailyReset)
	}
	if ctx.SummonsSinceDailyReset != 6 { // 9 - 3
		t.Errorf("SummonsSinceDailyReset = %d, want 6", ctx.SummonsSinceDailyReset)
	}
	if ctx.PurchasesSinceDailyReset != 0 { // 6 - 6
		t.Errorf("PurchasesSinceDailyReset = %d, want 0", ctx.PurchasesSinceDailyReset)
	}
	if ctx.ShopItemsBought != 5 { // 2+3, lifetime, not baselined
		t.Errorf("ShopItemsBought = %d, want 5", ctx.ShopItemsBought)
	}
	if !ctx.FavoriteCostumeSet {
		t.Error("FavoriteCostumeSet = false, want true")
	}
}

func TestBuildMissionEvalContextClampsNegative(t *testing.T) {
	// baseline above the current total must never produce negative progress.
	user := store.UserState{
		LifetimeCounters: store.LifetimeCountersState{SummonCount: 1},
		DailyMission:     store.DailyMissionState{SummonBaseline: 5, ClearBaseline: 5, PurchaseBaseline: 5},
	}

	ctx := BuildMissionEvalContext(user)

	if ctx.SummonsSinceDailyReset != 0 {
		t.Errorf("SummonsSinceDailyReset = %d, want 0", ctx.SummonsSinceDailyReset)
	}
	if ctx.ClearsSinceDailyReset != 0 {
		t.Errorf("ClearsSinceDailyReset = %d, want 0", ctx.ClearsSinceDailyReset)
	}
	if ctx.PurchasesSinceDailyReset != 0 {
		t.Errorf("PurchasesSinceDailyReset = %d, want 0", ctx.PurchasesSinceDailyReset)
	}
}

func TestDailyResetDue(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	period := gametime.StartOfDayPSTMillisAt(now)

	tests := []struct {
		name          string
		lastResetDate int64
		want          bool
	}{
		{"never reset", 0, true},
		{"reset before this period", period - 1, true},
		{"reset exactly at period start", period, false},
		{"reset later in this period", period + 1000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := store.UserState{DailyMission: store.DailyMissionState{LastResetDate: tt.lastResetDate}}
			if got := DailyResetDue(user, now); got != tt.want {
				t.Errorf("DailyResetDue = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyDailyReset(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	period := gametime.StartOfDayPSTMillisAt(now)

	user := store.UserState{
		Quests: map[int32]store.UserQuestState{
			101: {ClearCount: 5},
			102: {ClearCount: 3},
		},
		Missions:         map[int32]store.UserMissionState{},
		LifetimeCounters: store.LifetimeCountersState{SummonCount: 7, PurchaseCount: 4},
		DailyMission:     store.DailyMissionState{LastResetDate: 0},
	}

	ApplyDailyReset(&user, MissionCatalog{}, now)

	if user.DailyMission.ClearBaseline != 8 { // 5+3
		t.Errorf("ClearBaseline = %d, want 8", user.DailyMission.ClearBaseline)
	}
	if user.DailyMission.SummonBaseline != 7 {
		t.Errorf("SummonBaseline = %d, want 7", user.DailyMission.SummonBaseline)
	}
	if user.DailyMission.PurchaseBaseline != 4 {
		t.Errorf("PurchaseBaseline = %d, want 4", user.DailyMission.PurchaseBaseline)
	}
	if user.DailyMission.LastResetDate != period {
		t.Errorf("LastResetDate = %d, want %d", user.DailyMission.LastResetDate, period)
	}

	// all day-scoped progress must read zero.
	ctx := BuildMissionEvalContext(user)
	if ctx.ClearsSinceDailyReset != 0 || ctx.SummonsSinceDailyReset != 0 || ctx.PurchasesSinceDailyReset != 0 {
		t.Errorf("after reset, progress should be zero, got clears=%d summons=%d purchases=%d",
			ctx.ClearsSinceDailyReset, ctx.SummonsSinceDailyReset, ctx.PurchasesSinceDailyReset)
	}
}

func TestApplyDailyResetIsIdempotentWithinDay(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).UnixMilli()

	user := store.UserState{
		Quests:           map[int32]store.UserQuestState{},
		Missions:         map[int32]store.UserMissionState{},
		LifetimeCounters: store.LifetimeCountersState{SummonCount: 7},
		DailyMission:     store.DailyMissionState{LastResetDate: 0},
	}
	ApplyDailyReset(&user, MissionCatalog{}, now)

	// player summons twice more, then something calls reset again the same PST day.
	user.LifetimeCounters.SummonCount = 9
	sameDayLater := now + 2*60*60*1000
	ApplyDailyReset(&user, MissionCatalog{}, sameDayLater)

	if user.DailyMission.SummonBaseline != 7 {
		t.Errorf("SummonBaseline = %d, want 7 (must not re-snapshot within the same day)", user.DailyMission.SummonBaseline)
	}
	if got := BuildMissionEvalContext(user).SummonsSinceDailyReset; got != 2 {
		t.Errorf("SummonsSinceDailyReset = %d, want 2 (progress must survive a redundant reset call)", got)
	}
}

func TestApplyDailyResetRebaselinesOnNewDay(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).UnixMilli()

	user := store.UserState{
		Quests:           map[int32]store.UserQuestState{},
		Missions:         map[int32]store.UserMissionState{},
		LifetimeCounters: store.LifetimeCountersState{SummonCount: 7},
		DailyMission:     store.DailyMissionState{LastResetDate: 0},
	}
	ApplyDailyReset(&user, MissionCatalog{}, now)

	user.LifetimeCounters.SummonCount = 9
	nextDay := now + 24*60*60*1000
	ApplyDailyReset(&user, MissionCatalog{}, nextDay)

	if user.DailyMission.SummonBaseline != 9 {
		t.Errorf("SummonBaseline = %d, want 9 (new day must re-snapshot)", user.DailyMission.SummonBaseline)
	}
	if want := gametime.StartOfDayPSTMillisAt(nextDay); user.DailyMission.LastResetDate != want {
		t.Errorf("LastResetDate = %d, want %d", user.DailyMission.LastResetDate, want)
	}
	if got := BuildMissionEvalContext(user).SummonsSinceDailyReset; got != 0 {
		t.Errorf("SummonsSinceDailyReset = %d, want 0 after new-day reset", got)
	}
}

func TestApplyDailyResetDropsOnlyDailyMissionRecords(t *testing.T) {
	cat := MissionCatalog{ById: map[int32]MissionDef{
		10: {MissionId: 10, SubCategoryId: dailySubCategory}, // daily
		20: {MissionId: 20, SubCategoryId: 2},                // lifetime
	}}

	user := store.UserState{
		Quests: map[int32]store.UserQuestState{},
		Missions: map[int32]store.UserMissionState{
			10: {MissionId: 10, MissionProgressStatusType: 9}, // claimed daily
			20: {MissionId: 20, MissionProgressStatusType: 9}, // claimed lifetime
			30: {MissionId: 30, MissionProgressStatusType: 9}, // not in catalog
		},
		DailyMission: store.DailyMissionState{LastResetDate: 0},
	}

	ApplyDailyReset(&user, cat, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC).UnixMilli())

	if _, ok := user.Missions[10]; ok {
		t.Error("claimed daily mission record should be dropped so it re-arms")
	}
	if _, ok := user.Missions[20]; !ok {
		t.Error("claimed lifetime mission record must be preserved")
	}
	if _, ok := user.Missions[30]; !ok {
		t.Error("records not in the catalog must be left alone")
	}
}

func TestBuildMissionCatalogFrom(t *testing.T) {
	cat := buildMissionCatalogFrom(
		[]EntityMMission{
			{MissionId: 1, MissionGroupId: 100, MissionClearConditionType: 18, ClearConditionValue: 3, MissionRewardId: 500, MissionTermId: 7},
			{MissionId: 2, MissionGroupId: 200, MissionClearConditionType: 21, ClearConditionValue: 1},
		},
		[]EntityMMissionGroup{
			{MissionGroupId: 100, MissionCategoryType: 4, MissionSubCategoryId: dailySubCategory},
			{MissionGroupId: 200, MissionCategoryType: 4, MissionSubCategoryId: 2},
		},
		[]EntityMMissionReward{
			{MissionRewardId: 500, PossessionType: 1, PossessionId: 11, Count: 2},
			{MissionRewardId: 500, PossessionType: 1, PossessionId: 12, Count: 3}, // same id, two payouts
		},
		[]EntityMMissionTerm{{MissionTermId: 7, StartDatetime: 1000, EndDatetime: 2000}},
	)

	// group category/subcategory must be flattened onto the mission
	if got := cat.ById[1].SubCategoryId; got != dailySubCategory {
		t.Errorf("mission 1 SubCategoryId = %d, want %d", got, dailySubCategory)
	}
	if got := cat.ById[1].CategoryType; got != 4 {
		t.Errorf("mission 1 CategoryType = %d, want 4", got)
	}
	if got := len(cat.BySubCategory[dailySubCategory]); got != 1 {
		t.Errorf("daily bucket has %d missions, want 1", got)
	}
	if got := len(cat.ByGroup[100]); got != 1 {
		t.Errorf("group 100 has %d missions, want 1", got)
	}
	if got := len(cat.Rewards[500]); got != 2 {
		t.Errorf("reward 500 has %d payouts, want 2", got)
	}
	if !cat.TermActive(7, 1500) || cat.TermActive(7, 2000) {
		t.Error("term 7 window not indexed correctly")
	}
}
