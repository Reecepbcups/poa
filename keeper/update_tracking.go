package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/strangelove-ventures/poa"
)

// SetTotalValSetChange
func (k Keeper) SetTotalValSetChange(ctx context.Context, valSetChange uint64) {
	store := k.storeService.OpenKVStore(ctx)
	bz := sdk.Uint64ToBigEndian(valSetChange)
	store.Set(poa.TotalValSetChangeKey, bz)
}

func (k Keeper) GetTotalValSetChange(ctx context.Context) uint64 {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(poa.TotalValSetChangeKey)
	if err != nil {
		return 0
	}

	if bz == nil {
		return 0
	}

	return sdk.BigEndianToUint64(bz)
}

// TotalPrePowerUpdates
func (k Keeper) SetTotalPrePowerUpdates(ctx context.Context, valSetChange uint64) {
	store := k.storeService.OpenKVStore(ctx)
	bz := sdk.Uint64ToBigEndian(valSetChange)
	store.Set(poa.TotalPrePowerUpdates, bz)
}

func (k Keeper) GetTotalPrePowerUpdates(ctx context.Context) uint64 {
	store := k.storeService.OpenKVStore(ctx)
	bz, err := store.Get(poa.TotalPrePowerUpdates)
	if err != nil {
		return 0
	}

	if bz == nil {
		return 0
	}

	return sdk.BigEndianToUint64(bz)
}
