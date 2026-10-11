package service

import (
	"context"
	"math"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrPointsOperationConflict = infraerrors.Conflict("POINTS_OPERATION_CONFLICT", "Idempotency key was already used for a different points adjustment")
var ErrPointsReserved = infraerrors.BadRequest("POINTS_RESERVED", "Points are reserved by active requests; try again after they finish")
var ErrInvalidPointsAmount = infraerrors.BadRequest("INVALID_POINTS_AMOUNT", "Points must be finite and between 0 and 999999999999")

func (s *SettingService) PointsPerUSD(ctx context.Context) (float64, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyPointsPerUSD)
	if err != nil {
		return 0, err
	}
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil || rate < 0.00000001 || rate > 999999999999 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, infraerrors.BadRequest("INVALID_POINTS_RATE", "Configure a positive points-per-USD rate first")
	}
	return rate, nil
}

func (s *UserService) AdjustPoints(ctx context.Context, id int64, points float64, operation, operationID, notes string) (*User, error) {
	repo, ok := s.userRepo.(PointsRepository)
	if !ok {
		return nil, ErrBillingServiceUnavailable
	}
	if _, err := repo.AdjustPoints(ctx, id, points, operation, operationID, notes); err != nil {
		return nil, err
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, id)
	}
	return s.userRepo.GetByID(ctx, id)
}

func (s *UserService) PointsHistory(ctx context.Context, id int64, page, size int) ([]PointsLedgerEntry, int64, error) {
	repo, ok := s.userRepo.(PointsRepository)
	if !ok {
		return nil, 0, ErrBillingServiceUnavailable
	}
	return repo.ListPointsLedger(ctx, id, page, size)
}

func (s *UserService) PointsSummary(ctx context.Context, id int64) (*PointsSummary, error) {
	repo, ok := s.userRepo.(PointsRepository)
	if !ok {
		return nil, ErrBillingServiceUnavailable
	}
	return repo.PointsSummary(ctx, id)
}
