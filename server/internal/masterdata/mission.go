package masterdata

import (
	"log"
	"sync"

	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/utils"
)

const dailySubCategory int32 = 1

const (
	ClearConditionClearQuest    int32 = 1
	ClearConditionPerformSummon int32 = 18
	ClearConditionPurchaseItem  int32 = 21
	ClearConditionLogin         int32 = 23
	ClearConditionSetFavCostume int32 = 27
)

type MissionDef struct {
	MissionId                   int32
	GroupId                     int32
	CategoryType                int32
	SubCategoryId               int32
	ClearConditionType          int32
	ClearConditionGroupId       int32
	ClearConditionOptionGroupId int32
	ClearConditionValue         int32
	RewardId                    int32
	TermId                      int32
	SortOrder                   int32
}

type MissionRewardDef struct {
	PossessionType int32
	PossessionId   int32
	Count          int32
}

type MissionTerm struct {
	Start int64
	End   int64
}

type MissionCatalog struct {
	ById          map[int32]MissionDef
	ByGroup       map[int32][]MissionDef
	BySubCategory map[int32][]MissionDef
	Rewards       map[int32][]MissionRewardDef
	Terms         map[int32]MissionTerm
}

func (c MissionCatalog) TermActive(termId int32, nowMillis int64) bool {
	t, ok := c.Terms[termId]
	if !ok {
		return true
	}
	return nowMillis >= t.Start && nowMillis < t.End
}

func (c MissionCatalog) DailyMissions() []MissionDef {
	return c.BySubCategory[dailySubCategory]
}

func readMissionTable[T any](name, what string) ([]T, bool) {
	rows, err := utils.ReadTable[T](name)
	if err != nil {
		log.Printf("[mission] %s unavailable, %s empty: %v", name, what, err)
		return nil, false
	}
	return rows, true
}

var missionCatalogOnce = sync.OnceValue(buildMissionCatalog)

func MissionCatalogCached() MissionCatalog {
	return missionCatalogOnce()
}

func buildMissionCatalog() MissionCatalog {
	missions, _ := readMissionTable[EntityMMission]("m_mission", "missions")
	groups, _ := readMissionTable[EntityMMissionGroup]("m_mission_group", "mission groups")
	rewards, _ := readMissionTable[EntityMMissionReward]("m_mission_reward", "mission rewards")
	terms, _ := readMissionTable[EntityMMissionTerm]("m_mission_term", "mission terms")
	return buildMissionCatalogFrom(missions, groups, rewards, terms)
}

func buildMissionCatalogFrom(
	missions []EntityMMission,
	groups []EntityMMissionGroup,
	rewards []EntityMMissionReward,
	terms []EntityMMissionTerm,
) MissionCatalog {
	cat := MissionCatalog{
		ById:          make(map[int32]MissionDef),
		ByGroup:       make(map[int32][]MissionDef),
		BySubCategory: make(map[int32][]MissionDef),
		Rewards:       make(map[int32][]MissionRewardDef),
		Terms:         make(map[int32]MissionTerm),
	}
	groupById := make(map[int32]EntityMMissionGroup, len(groups))
	for _, g := range groups {
		groupById[g.MissionGroupId] = g
	}

	for _, r := range rewards {
		cat.Rewards[r.MissionRewardId] = append(cat.Rewards[r.MissionRewardId], MissionRewardDef{
			PossessionType: r.PossessionType,
			PossessionId:   r.PossessionId,
			Count:          r.Count,
		})
	}

	for _, t := range terms {
		cat.Terms[t.MissionTermId] = MissionTerm{Start: t.StartDatetime, End: t.EndDatetime}
	}

	for _, m := range missions {
		g := groupById[m.MissionGroupId] // zero value if group missing → category/sub stay
		def := MissionDef{
			MissionId:                   m.MissionId,
			GroupId:                     m.MissionGroupId,
			CategoryType:                g.MissionCategoryType,
			SubCategoryId:               g.MissionSubCategoryId,
			ClearConditionType:          m.MissionClearConditionType,
			ClearConditionGroupId:       m.MissionClearConditionGroupId,
			ClearConditionOptionGroupId: m.MissionClearConditionOptionGroupId,
			ClearConditionValue:         m.ClearConditionValue,
			RewardId:                    m.MissionRewardId,
			TermId:                      m.MissionTermId,
			SortOrder:                   m.SortOrderInMissionGroup,
		}
		cat.ById[def.MissionId] = def
		cat.ByGroup[def.GroupId] = append(cat.ByGroup[def.GroupId], def)
		cat.BySubCategory[def.SubCategoryId] = append(cat.BySubCategory[def.SubCategoryId], def)
	}

	return cat
}

// TODO: derive from m_mission_clear_condition* rather than a static list.
var anyQuestClearGroups = map[int32]bool{9: true, 4203: true}

type MissionEvalContext struct {
	ClearsSinceDailyReset    int32
	PurchasesSinceDailyReset int32
	SummonsSinceDailyReset   int32
	ShopItemsBought          int32
	FavoriteCostumeSet       bool
}

func clampNonNeg(n int32) int32 {
	if n < 0 {
		return 0
	}
	return n
}

func BuildMissionEvalContext(user store.UserState) MissionEvalContext {
	var lifetimeClears, bought int32
	for _, q := range user.Quests {
		lifetimeClears += q.ClearCount
	}
	for _, si := range user.ShopItems {
		bought += si.BoughtCount
	}
	return MissionEvalContext{
		ClearsSinceDailyReset:    clampNonNeg(lifetimeClears - user.DailyMission.ClearBaseline),
		PurchasesSinceDailyReset: clampNonNeg(user.LifetimeCounters.PurchaseCount - user.DailyMission.PurchaseBaseline),
		SummonsSinceDailyReset:   clampNonNeg(user.LifetimeCounters.SummonCount - user.DailyMission.SummonBaseline),
		ShopItemsBought:          bought,
		FavoriteCostumeSet:       user.Profile.FavoriteCostumeId != 0,
	}
}

func EvaluateMission(user store.UserState, def MissionDef) (progress, status int32) {
	return EvaluateMissionCtx(BuildMissionEvalContext(user), def)
}

func EvaluateMissionCtx(ctx MissionEvalContext, def MissionDef) (progress, status int32) {
	target := def.ClearConditionValue
	if target < 1 {
		target = 1
	}
	daily := def.SubCategoryId == dailySubCategory

	switch def.ClearConditionType {
	case ClearConditionClearQuest:
		// Only count unconstrained "any quest" groups; a non-zero optGroupId means
		// an extra constraint (difficulty, etc.) we can't evaluate yet.
		if !anyQuestClearGroups[def.ClearConditionGroupId] || def.ClearConditionOptionGroupId != 0 {
			return 0, int32(model.MissionProgressStatusTypeInProgress)
		}
		progress = ctx.ClearsSinceDailyReset
	case ClearConditionPurchaseItem:
		if daily {
			progress = ctx.PurchasesSinceDailyReset
		} else {
			progress = ctx.ShopItemsBought
		}
	case ClearConditionPerformSummon:
		if daily {
			progress = ctx.SummonsSinceDailyReset
		}
	case ClearConditionLogin:
		if daily {
			progress = target
		}
	case ClearConditionSetFavCostume:
		if ctx.FavoriteCostumeSet {
			progress = target
		}
	default:
		return 0, int32(model.MissionProgressStatusTypeInProgress)
	}

	if progress >= target {
		return target, int32(model.MissionProgressStatusTypeClear)
	}
	return progress, int32(model.MissionProgressStatusTypeInProgress)
}

func DailyResetDue(user store.UserState, nowMillis int64) bool {
	return user.DailyMission.LastResetDate < gametime.StartOfDayPSTMillisAt(nowMillis)
}

func ApplyDailyReset(user *store.UserState, cat MissionCatalog, nowMillis int64) {
	period := gametime.StartOfDayPSTMillisAt(nowMillis)
	if user.DailyMission.LastResetDate >= period {
		return
	}

	// snapshot current totals so "since reset" reads 0.
	var lifetimeClears int32
	for _, q := range user.Quests {
		lifetimeClears += q.ClearCount
	}
	user.DailyMission.ClearBaseline = lifetimeClears
	user.DailyMission.SummonBaseline = user.LifetimeCounters.SummonCount
	user.DailyMission.PurchaseBaseline = user.LifetimeCounters.PurchaseCount

	for id := range user.Missions {
		if def, ok := cat.ById[id]; ok && def.SubCategoryId == dailySubCategory {
			delete(user.Missions, id)
		}
	}

	user.DailyMission.LastResetDate = period
}
