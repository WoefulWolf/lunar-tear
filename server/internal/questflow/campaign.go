package questflow

import (
	"lunar-tear/server/internal/campaign"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/timeline"
)

func (h *QuestHandler) targetForMain(questId int32) campaign.QuestTarget {
	return campaign.QuestTarget{
		QuestId:   questId,
		QuestType: campaign.QuestTypeMainQuest,
		ChapterId: h.MainQuestChapterIdByQuestId[questId],
	}
}

func (h *QuestHandler) targetForEvent(eventChapterId, questId int32) campaign.QuestTarget {
	return campaign.QuestTarget{
		QuestId:        questId,
		QuestType:      campaign.QuestTypeEventQuest,
		EventQuestType: h.EventQuestTypeByChapterId[eventChapterId],
		ChapterId:      eventChapterId,
	}
}

func (h *QuestHandler) targetForExtra(questId int32) campaign.QuestTarget {
	return campaign.QuestTarget{QuestId: questId, QuestType: campaign.QuestTypeExtraQuest}
}

func (h *QuestHandler) targetForBigHunt(questId int32) campaign.QuestTarget {
	return campaign.QuestTarget{QuestId: questId, QuestType: campaign.QuestTypeBigHunt}
}

func (h *QuestHandler) campaignFilter(user *store.UserState) campaign.Filter {
	return campaign.Filter{Now: timeline.NowFor(user.RegisterDatetime), UserStatus: campaign.TargetUserStatusAll}
}

func (h *QuestHandler) staminaWithCampaign(user *store.UserState, baseStamina int32, t campaign.QuestTarget) int32 {
	if h.Campaigns == nil {
		return baseStamina
	}
	return h.Campaigns.QuestStamina(t, h.campaignFilter(user)).Apply(baseStamina)
}

func (h *QuestHandler) appendBonusDrops(user *store.UserState, drops []RewardGrant, t campaign.QuestTarget) []RewardGrant {
	if h.Campaigns == nil {
		return drops
	}
	for _, bd := range h.Campaigns.QuestBonusDrops(t, h.campaignFilter(user)) {
		drops = append(drops, RewardGrant{
			PossessionType: model.PossessionType(bd.PossessionType),
			PossessionId:   bd.PossessionId,
			Count:          bd.Count,
		})
	}
	return drops
}
