package service

import (
	"context"
	"fmt"
	"log"
	"os"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/userdata"

	"google.golang.org/protobuf/types/known/emptypb"
)

// masterDataBinPath is the canonical location of the encrypted master data
// file. The mtime of this file is folded into the version string so the
// client invalidates its cache as soon as an admin reload swaps it in.
const masterDataBinPath = "assets/release/20240404193219.bin.e"

// masterDataBaseVersion preserves the historical "yyyymmddHHMMSS" value the
// client has always seen; we suffix it with the file mtime to force a
// re-download when content changes.
const masterDataBaseVersion = "20240404193219"

type DataServiceServer struct {
	pb.UnimplementedDataServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewDataServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *DataServiceServer {
	return &DataServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *DataServiceServer) GetLatestMasterDataVersion(ctx context.Context, _ *emptypb.Empty) (*pb.MasterDataGetLatestVersionResponse, error) {
	version := masterDataBaseVersion
	if info, err := os.Stat(masterDataBinPath); err == nil {
		version = fmt.Sprintf("%s_%d", masterDataBaseVersion, info.ModTime().UnixMilli())
	} else {
		log.Printf("[DataService] stat %s: %v (falling back to base version)", masterDataBinPath, err)
	}
	log.Printf("[DataService] GetLatestMasterDataVersion -> %s", version)
	return &pb.MasterDataGetLatestVersionResponse{
		LatestMasterDataVersion: version,
	}, nil
}

func (s *DataServiceServer) GetUserDataNameV2(ctx context.Context, _ *emptypb.Empty) (*pb.UserDataGetNameResponseV2, error) {
	log.Printf("[DataService] GetUserDataNameV2")
	return &pb.UserDataGetNameResponseV2{
		TableNameList: []*pb.TableNameList{
			{TableName: defaultTableNames()},
		},
	}, nil
}

func (s *DataServiceServer) GetUserData(ctx context.Context, req *pb.UserDataGetRequest) (*pb.UserDataGetResponse, error) {
	log.Printf("[DataService] GetUserData: tables=%v", req.TableName)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	user, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("snapshot user: %w", err)
	}

	now := gametime.NowMillis()
	catalog := masterdata.MissionCatalogCached()
	if masterdata.DailyResetDue(user, now) {
		itemShopPool := s.holder.Get().Shop.ItemShopPool
		user, err = s.users.UpdateUser(userId, func(u *store.UserState) {
			// Restock first: ApplyDailyReset stamps LastResetDate, which is what
			// gates this whole block.
			masterdata.ApplyShopDailyRestock(u, itemShopPool, now)
			masterdata.ApplyDailyReset(u, catalog, now)
		})
		if err != nil {
			return nil, fmt.Errorf("daily reset: %w", err)
		}
	}

	result := userdata.ProjectTables(user, req.TableName)
	return &pb.UserDataGetResponse{
		UserDataJson: result,
	}, nil
}

func defaultTableNames() []string {
	return []string{
		"IUser",
		"IUserApple",
		"IUserAutoSaleSettingDetail",
		"IUserBeginnerCampaign",
		"IUserBigHuntMaxScore",
		"IUserBigHuntProgressStatus",
		"IUserBigHuntScheduleMaxScore",
		"IUserBigHuntStatus",
		"IUserBigHuntWeeklyMaxScore",
		"IUserBigHuntWeeklyStatus",
		"IUserCageOrnamentReward",
		"IUserCharacter",
		"IUserCharacterBoard",
		"IUserCharacterBoardAbility",
		"IUserCharacterBoardCompleteReward",
		"IUserCharacterBoardStatusUp",
		"IUserCharacterCostumeLevelBonus",
		"IUserCharacterRebirth",
		"IUserCharacterViewerField",
		"IUserComebackCampaign",
		"IUserCompanion",
		"IUserConsumableItem",
		"IUserContentsStory",
		"IUserCostume",
		"IUserCostumeActiveSkill",
		"IUserCostumeAwakenStatusUp",
		"IUserCostumeLevelBonusReleaseStatus",
		"IUserCostumeLotteryEffect",
		"IUserCostumeLotteryEffectAbility",
		"IUserCostumeLotteryEffectPending",
		"IUserCostumeLotteryEffectStatusUp",
		"IUserDeck",
		"IUserDeckCharacter",
		"IUserDeckCharacterDressupCostume",
		"IUserDeckLimitContentRestricted",
		"IUserDeckPartsGroup",
		"IUserDeckSubWeaponGroup",
		"IUserDeckTypeNote",
		"IUserDokan",
		"IUserEventQuestDailyGroupCompleteReward",
		"IUserEventQuestGuerrillaFreeOpen",
		"IUserEventQuestLabyrinthSeason",
		"IUserEventQuestLabyrinthStage",
		"IUserEventQuestProgressStatus",
		"IUserEventQuestTowerAccumulationReward",
		"IUserExplore",
		"IUserExploreScore",
		"IUserExtraQuestProgressStatus",
		"IUserFacebook",
		"IUserGem",
		"IUserGimmick",
		"IUserGimmickOrnamentProgress",
		"IUserGimmickSequence",
		"IUserGimmickUnlock",
		"IUserImportantItem",
		"IUserLimitedOpen",
		// "IUserLogin",
		"IUserLoginBonus",
		"IUserMainQuestFlowStatus",
		"IUserMainQuestMainFlowStatus",
		"IUserMainQuestProgressStatus",
		"IUserMainQuestReplayFlowStatus",
		"IUserMainQuestSeasonRoute",
		"IUserMaterial",
		"IUserMission",
		"IUserMissionCompletionProgress",
		"IUserMissionPassPoint",
		"IUserMovie",
		"IUserNaviCutIn",
		"IUserOmikuji",
		"IUserParts",
		"IUserPartsGroupNote",
		"IUserPartsPreset",
		"IUserPartsPresetTag",
		"IUserPartsStatusSub",
		"IUserPortalCageStatus",
		"IUserPossessionAutoConvert",
		"IUserPremiumItem",
		"IUserProfile",
		"IUserPvpDefenseDeck",
		"IUserPvpStatus",
		"IUserPvpWeeklyResult",
		"IUserQuest",
		"IUserQuestAutoOrbit",
		"IUserQuestLimitContentStatus",
		"IUserQuestMission",
		"IUserQuestReplayFlowRewardGroup",
		"IUserQuestSceneChoice",
		"IUserQuestSceneChoiceHistory",
		// "IUserSetting",
		"IUserShopItem",
		"IUserShopReplaceable",
		"IUserShopReplaceableLineup",
		"IUserSideStoryQuest",
		"IUserSideStoryQuestSceneProgressStatus",
		"IUserStatus",
		"IUserThought",
		"IUserTripleDeck",
		"IUserTutorialProgress",
		"IUserWeapon",
		"IUserWeaponAbility",
		"IUserWeaponAwaken",
		"IUserWeaponNote",
		"IUserWeaponSkill",
		"IUserWeaponStory",
		"IUserWebviewPanelMission",
	}
}
