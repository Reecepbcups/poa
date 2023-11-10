package module

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (am AppModule) BeginBlocker(ctx context.Context) error {
	vals, err := am.keeper.GetStakingKeeper().GetAllValidators(ctx)
	if err != nil {
		return err
	}

	// Reset the block cache
	// if v := am.keeper.GetTotalValSetChange(ctx); v != 0 {
	// 	am.keeper.SetTotalValSetChange(ctx, 0)
	// }

	// Sets this blocks cached total Validator power (used to validate we do not set >33% of total power in 1 block)
	// This may not be needed? (if we can hold off on SetLastTotalPower until the next begin block? (i don't think so though))
	// if last, err := am.keeper.GetStakingKeeper().GetLastTotalPower(ctx); err != nil {
	// 	return err
	// } else {
	// 	// Update the cache
	// 	if am.keeper.GetTotalPrePowerUpdates(ctx) != last.Uint64() {
	// 		am.keeper.SetTotalPrePowerUpdates(ctx, last.Uint64())
	// 	}
	// }

	for _, v := range vals {
		switch v.GetStatus() {

		case stakingtypes.Unbonding:
			v.Status = stakingtypes.Unbonded
			if err := am.keeper.GetStakingKeeper().SetValidator(ctx, v); err != nil {
				return err
			}
		case stakingtypes.Unbonded:
			valAddr, err := sdk.ValAddressFromBech32(v.OperatorAddress)
			if err != nil {
				return err
			}

			if err := am.keeper.GetStakingKeeper().DeleteLastValidatorPower(ctx, valAddr); err != nil {
				return err
			}
		}
	}

	return nil
}
