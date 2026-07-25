package service

import (
	"context"
	"fmt"
	"log"

	pb "lunar-tear/server/gen/proto"
	"lunar-tear/server/internal/gametime"
	"lunar-tear/server/internal/masterdata"
	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/runtime"
	"lunar-tear/server/internal/store"

	"google.golang.org/protobuf/types/known/emptypb"
)

type ShopServiceServer struct {
	pb.UnimplementedShopServiceServer
	users    store.UserRepository
	sessions store.SessionRepository
	holder   *runtime.Holder
}

func NewShopServiceServer(users store.UserRepository, sessions store.SessionRepository, holder *runtime.Holder) *ShopServiceServer {
	return &ShopServiceServer{users: users, sessions: sessions, holder: holder}
}

func (s *ShopServiceServer) Buy(ctx context.Context, req *pb.BuyRequest) (*pb.BuyResponse, error) {
	log.Printf("[ShopService] Buy: shopId=%d items=%v", req.ShopId, req.ShopItems)

	cat := s.holder.Get()
	catalog := cat.Shop
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		for shopItemId, qty := range req.ShopItems {
			item, ok := catalog.Items[shopItemId]
			if !ok {
				log.Printf("[ShopService] Buy: unknown shopItemId=%d, skipping", shopItemId)
				continue
			}

			totalPrice := item.Price * qty
			if err := store.DeductPrice(user, item.PriceType, item.PriceId, totalPrice); err != nil {
				log.Printf("[ShopService] Buy: deduct failed shopItemId=%d: %v", shopItemId, err)
				continue
			}

			for _, content := range catalog.Contents[shopItemId] {
				granter.GrantFull(user,
					model.PossessionType(content.PossessionType),
					content.PossessionId,
					content.Count*qty,
					nowMillis,
				)
			}

			applyShopContentEffects(catalog, user, shopItemId, qty, nowMillis)

			si := user.ShopItems[shopItemId]
			si.ShopItemId = shopItemId
			si.BoughtCount += qty
			si.LatestBoughtCountChangedDatetime = nowMillis
			si.LatestVersion = nowMillis
			user.ShopItems[shopItemId] = si
			user.LifetimeCounters.PurchaseCount += qty
		}
	})
	if err != nil {
		return nil, fmt.Errorf("shop buy: %w", err)
	}
	return &pb.BuyResponse{
		OverflowPossession: []*pb.Possession{},
	}, nil
}

func (s *ShopServiceServer) RefreshUserData(ctx context.Context, req *pb.RefreshRequest) (*pb.RefreshResponse, error) {
	log.Printf("[ShopService] RefreshUserData: isGemUsed=%v", req.IsGemUsed)

	catalog := s.holder.Get().Shop
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		if len(user.ShopReplaceableLineup) == 0 && len(catalog.ItemShopPool) > 0 {
			for i, itemId := range catalog.ItemShopPool {
				slot := int32(i + 1)
				user.ShopReplaceableLineup[slot] = store.UserShopReplaceableLineupState{
					SlotNumber:    slot,
					ShopItemId:    itemId,
					LatestVersion: nowMillis,
				}
			}
		}
		if req.IsGemUsed {
			user.ShopReplaceable.LineupUpdateCount++
			user.ShopReplaceable.LatestLineupUpdateDatetime = nowMillis
			for _, itemId := range catalog.ItemShopPool {
				if si, ok := user.ShopItems[itemId]; ok {
					si.BoughtCount = 0
					si.LatestVersion = nowMillis
					user.ShopItems[itemId] = si
				}
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("shop refresh: %w", err)
	}

	return &pb.RefreshResponse{}, nil
}

func (s *ShopServiceServer) GetCesaLimit(_ context.Context, _ *emptypb.Empty) (*pb.GetCesaLimitResponse, error) {
	log.Printf("[ShopService] GetCesaLimit")
	return &pb.GetCesaLimitResponse{
		CesaLimit: []*pb.CesaLimit{},
	}, nil
}

func (s *ShopServiceServer) CreatePurchaseTransaction(ctx context.Context, req *pb.CreatePurchaseTransactionRequest) (*pb.CreatePurchaseTransactionResponse, error) {
	log.Printf("[ShopService] CreatePurchaseTransaction: shopId=%d shopItemId=%d productId=%s",
		req.ShopId, req.ShopItemId, req.ProductId)

	cat := s.holder.Get()
	catalog := cat.Shop
	granter := cat.QuestHandler.Granter
	userId := CurrentUserId(ctx, s.users, s.sessions)
	nowMillis := gametime.NowMillis()

	_, err := s.users.UpdateUser(userId, func(user *store.UserState) {
		item, ok := catalog.Items[req.ShopItemId]
		if !ok {
			log.Printf("[ShopService] CreatePurchaseTransaction: unknown shopItemId=%d", req.ShopItemId)
			return
		}

		if err := store.DeductPrice(user, item.PriceType, item.PriceId, item.Price); err != nil {
			log.Printf("[ShopService] CreatePurchaseTransaction: deduct failed: %v", err)
		}

		for _, content := range catalog.Contents[req.ShopItemId] {
			granter.GrantFull(user,
				model.PossessionType(content.PossessionType),
				content.PossessionId,
				content.Count,
				nowMillis,
			)
		}

		applyShopContentEffects(catalog, user, req.ShopItemId, 1, nowMillis)

		si := user.ShopItems[req.ShopItemId]
		si.ShopItemId = req.ShopItemId
		si.BoughtCount++
		if item.ShopItemLimitedStockId > 0 {
			if maxCount, ok := catalog.LimitedStock[item.ShopItemLimitedStockId]; ok && si.BoughtCount >= maxCount {
				si.BoughtCount = 0
			}
		}
		si.LatestBoughtCountChangedDatetime = nowMillis
		si.LatestVersion = nowMillis
		user.ShopItems[req.ShopItemId] = si
	})
	if err != nil {
		return nil, fmt.Errorf("create purchase transaction: %w", err)
	}

	txId := fmt.Sprintf("tx_%d_%d_%d", userId, req.ShopItemId, nowMillis)

	return &pb.CreatePurchaseTransactionResponse{
		PurchaseTransactionId: txId,
	}, nil
}

func (s *ShopServiceServer) PurchaseGooglePlayStoreProduct(ctx context.Context, req *pb.PurchaseGooglePlayStoreProductRequest) (*pb.PurchaseGooglePlayStoreProductResponse, error) {
	log.Printf("[ShopService] PurchaseGooglePlayStoreProduct: txId=%s", req.PurchaseTransactionId)

	userId := CurrentUserId(ctx, s.users, s.sessions)
	_, err := s.users.LoadUser(userId)
	if err != nil {
		return nil, fmt.Errorf("purchase google play: %w", err)
	}

	return &pb.PurchaseGooglePlayStoreProductResponse{
		OverflowPossession: []*pb.Possession{},
	}, nil
}

func applyShopContentEffects(catalog *masterdata.ShopCatalog, user *store.UserState, shopItemId, qty int32, nowMillis int64) {
	for _, effect := range catalog.Effects[shopItemId] {
		switch effect.EffectTargetType {
		case model.EffectTargetStaminaRecovery:
			maxMillis := catalog.MaxStaminaMillis[user.Status.Level]
			millis := store.ResolveStaminaEffectMillis(effect.EffectValueType, effect.EffectValue, maxMillis)
			store.RecoverStamina(user, millis*qty, maxMillis, nowMillis)
		default:
			log.Printf("[ShopService] unhandled effect: shopItemId=%d targetType=%d", shopItemId, effect.EffectTargetType)
		}
	}
}
