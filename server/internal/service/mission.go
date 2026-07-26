package service

import (
	"context"
	"fmt"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
	"lunar-tear/server/internal/timeline"
)

type MissionServiceServer struct {
	pb.UnimplementedMissionServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
}

func NewMissionServiceServer(users store.UserRepository, sessions store.SessionRepository) *MissionServiceServer {
	return &MissionServiceServer{users: users, sessions: sessions}
}

func (s *MissionServiceServer) UpdateMissionProgress(ctx context.Context, req *pb.UpdateMissionProgressRequest) (*pb.UpdateMissionProgressResponse, error) {
	log.Printf("[MissionService] UpdateMissionProgress: cage=%v pictureBook=%v", req.CageMeasurableValues, req.PictureBookMeasurableValues)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	_, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("snapshot user: %w", err)
	}

	return &pb.UpdateMissionProgressResponse{}, nil
}

func (s *MissionServiceServer) ReceiveMissionRewardsById(ctx context.Context, req *pb.ReceiveMissionRewardsByIdRequest) (*pb.ReceiveMissionRewardsResponse, error) {
	log.Printf("[MissionService] ReceiveMissionRewardsById: ids=%v", req.MissionId)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	cat := masterdata.MissionCatalogCached()
	nowMillis := gametime.NowMillis()

	var received []*pb.MissionReward
	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		user.EnsureMaps()
		// Term windows use the player's clock; the timestamps below are real time.
		contentNow := timeline.NowFor(user.RegisterDatetime)
		for _, missionId := range req.MissionId {
			def, ok := cat.ById[missionId]
			if !ok {
				log.Printf("[MissionService] claim: unknown mission %d", missionId)
				continue
			}
			if existing, ok := user.Missions[missionId]; ok &&
				existing.MissionProgressStatusType >= int32(model.MissionProgressStatusTypeRewardReceived) {
				continue
			}
			if !cat.TermActive(def.TermId, contentNow) {
				continue
			}

			progress, status := masterdata.EvaluateMission(*user, def)
			if status < int32(model.MissionProgressStatusTypeClear) {
				log.Printf("[MissionService] claim rejected: mission %d not cleared (condType=%d progress=%d/%d)",
					missionId, def.ClearConditionType, progress, def.ClearConditionValue)
				continue
			}

			for _, r := range cat.Rewards[def.RewardId] {
				store.GrantPossession(user, model.PossessionType(r.PossessionType), r.PossessionId, r.Count)
				received = append(received, &pb.MissionReward{
					PossessionType: r.PossessionType,
					PossessionId:   r.PossessionId,
					Count:          r.Count,
				})
			}

			startDatetime := nowMillis
			if existing, ok := user.Missions[missionId]; ok && existing.StartDatetime != 0 {
				startDatetime = existing.StartDatetime
			}
			user.Missions[missionId] = store.UserMissionState{
				MissionId:                 missionId,
				StartDatetime:             startDatetime,
				ProgressValue:             progress,
				MissionProgressStatusType: int32(model.MissionProgressStatusTypeRewardReceived),
				ClearDatetime:             nowMillis,
				LatestVersion:             nowMillis,
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}

	return &pb.ReceiveMissionRewardsResponse{ReceivedPossession: received}, nil
}
