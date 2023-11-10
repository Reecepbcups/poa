package keeper

import (
	"context"
	"fmt"
	"math"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/strangelove-ventures/poa"
)

var _ poa.MsgServer = msgServer{}

type msgServer struct {
	k Keeper
}

// NewMsgServerImpl returns an implementation of the module MsgServer interface.
func NewMsgServerImpl(keeper Keeper) poa.MsgServer {
	return &msgServer{k: keeper}
}

func (ms msgServer) CheckChangeValidatorsPercentForIBC(ctx context.Context) error {
	// 33% of the total set power must not be updated within the trusting period
	// for IBC light clients to be able to verify the validator set.
	maxUpdateLimit := ms.k.GetTotalPrePowerUpdates(ctx) / 3
	valSetUpdated := ms.k.GetTotalValSetChange(ctx)

	fmt.Println("maxUpdateLimit", maxUpdateLimit)
	fmt.Println("valSetUpdated", valSetUpdated)

	if maxUpdateLimit == 0 {
		// set it with the
		return nil
	}

	if valSetUpdated >= maxUpdateLimit {
		// If the total power change is greater than 33% of the total set power in this block, return an error.
		return poa.ErrTooManyValidatorsChanged
	}

	return nil
}

func (ms msgServer) SetPower(ctx context.Context, msg *poa.MsgSetPower) (*poa.MsgSetPowerResponse, error) {
	if ok := ms.isAdmin(ctx, msg.Sender); !ok {
		return nil, poa.ErrNotAnAuthority
	}

	if err := msg.Validate(ms.k.validatorAddressCodec); err != nil {
		return nil, err
	}

	// accepts a validator into the active set if they are pending approval.
	if isPending, err := ms.k.IsValidatorPending(ctx, msg.ValidatorAddress); err != nil {
		return nil, err
	} else if isPending {
		if err := ms.acceptNewValidator(ctx, msg.ValidatorAddress, msg.Power); err != nil {
			return nil, err
		}
	}

	// If the message is not flagged as unsafe, validate the power & IBC checks
	if !msg.Unsafe {
		totalPOAPower := sdkmath.ZeroInt()
		allDelegations, err := ms.k.stakingKeeper.GetAllDelegations(ctx)
		if err != nil {
			return nil, err
		}

		for _, del := range allDelegations {
			totalPOAPower = totalPOAPower.Add(del.Shares.TruncateInt())
		}

		// Verify the new set power is not >30% of the set.
		if msg.Power > totalPOAPower.Mul(sdkmath.NewInt(30)).Quo(sdkmath.NewInt(100)).Uint64() {
			return nil, poa.ErrUnsafePower
		}
	}

	valAddr, err := sdk.ValAddressFromBech32(msg.ValidatorAddress)
	if err != nil {
		return nil, sdkerrors.ErrInvalidAddress.Wrapf("invalid validator address: %s", err)
	}

	// validatorTokens, err := ms.k.stakingKeeper.GetValidatorUpdates()

	val, err := ms.k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return nil, err
	}

	fmt.Println("val.Tokens", val.Tokens)

	// sets the new POA power for the validator
	if _, err := ms.updatePOAPower(ctx, msg.ValidatorAddress, int64(msg.Power)); err != nil {
		return nil, err
	}

	// totalDiff := math.Abs(msg.Power - uint64(previousPower))
	totalDiff := uint64(math.Abs(float64(msg.Power) - float64(val.Tokens.Int64())))

	fmt.Println("previousPower", val.Tokens)
	fmt.Println("totalDiff", totalDiff)
	fmt.Println("msg.Power", msg.Power)

	// Update the validator set changes for this block.
	// updatedPower := ms.k.GetTotalValSetChange(ctx) + math.NewIntFromUint64(msg.Power).SubRaw(previousPower).Uint64()
	updatedPower := ms.k.GetTotalValSetChange(ctx) + totalDiff
	ms.k.SetTotalValSetChange(ctx, updatedPower)

	if !msg.Unsafe {
		// Verify the total changed power of this set migration is not >33% of the set as
		// this would break IBC light-clients.
		if err := ms.CheckChangeValidatorsPercentForIBC(ctx); err != nil {
			return nil, err
		}
	}

	return &poa.MsgSetPowerResponse{}, nil
}

func (ms msgServer) RemoveValidator(ctx context.Context, msg *poa.MsgRemoveValidator) (*poa.MsgRemoveValidatorResponse, error) {
	if ok := ms.isAdmin(ctx, msg.Sender); !ok {
		return nil, poa.ErrNotAnAuthority
	}

	// Ensure we do not remove the last validator in the set.
	allValidators, err := ms.k.stakingKeeper.GetAllValidators(ctx)
	if err != nil {
		return nil, err
	}
	if len(allValidators) == 1 {
		return nil, fmt.Errorf("cannot remove the last validator")
	}

	val, err := ms.updatePOAPower(ctx, msg.ValidatorAddress, 0)
	if err != nil {
		return nil, err
	}

	// clear missed blocks (is this needed?)
	cons, err := val.GetConsAddr()
	if err != nil {
		return nil, err
	}
	if err := ms.k.slashKeeper.DeleteMissedBlockBitmap(ctx, sdk.ConsAddress(cons)); err != nil {
		return nil, err
	}
	if err := ms.k.slashKeeper.SetValidatorSigningInfo(ctx, sdk.ConsAddress(cons), slashingtypes.ValidatorSigningInfo{}); err != nil {
		return nil, err
	}

	return &poa.MsgRemoveValidatorResponse{}, nil
}

// pulled from x/staking
func (ms msgServer) CreateValidator(ctx context.Context, msg *poa.MsgCreateValidator) (*poa.MsgCreateValidatorResponse, error) {
	valAddr, err := ms.k.validatorAddressCodec.StringToBytes(msg.ValidatorAddress)
	if err != nil {
		return nil, sdkerrors.ErrInvalidAddress.Wrapf("invalid validator address: %s", err)
	}

	if err := msg.Validate(ms.k.validatorAddressCodec); err != nil {
		return nil, err
	}

	minCommRate, err := ms.k.stakingKeeper.MinCommissionRate(ctx)
	if err != nil {
		return nil, err
	}

	if msg.Commission.Rate.LT(minCommRate) {
		return nil, errorsmod.Wrapf(stakingtypes.ErrCommissionLTMinRate, "cannot set validator commission to less than minimum rate of %s", minCommRate)
	}

	// check to see if the pubkey or sender has been registered before
	if _, err := ms.k.stakingKeeper.GetValidator(ctx, valAddr); err == nil {
		return nil, stakingtypes.ErrValidatorOwnerExists
	}

	pk, ok := msg.Pubkey.GetCachedValue().(cryptotypes.PubKey)
	if !ok {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidType, "CreateValidator expecting cryptotypes.PubKey, got %T. developer note: make sure to impl codectypes.UnpackInterfacesMessage", pk)
	}

	if _, err := ms.k.stakingKeeper.GetValidatorByConsAddr(ctx, sdk.GetConsAddress(pk)); err == nil {
		return nil, stakingtypes.ErrValidatorPubKeyExists
	}

	if _, err := msg.Description.EnsureLength(); err != nil {
		return nil, err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	cp := sdkCtx.ConsensusParams()
	if cp.Validator != nil {
		pkType := pk.Type()
		hasKeyType := false
		for _, keyType := range cp.Validator.PubKeyTypes {
			if pkType == keyType {
				hasKeyType = true
				break
			}
		}
		if !hasKeyType {
			return nil, errorsmod.Wrapf(
				stakingtypes.ErrValidatorPubKeyTypeNotSupported,
				"got: %s, expected: %s", pk.Type(), cp.Validator.PubKeyTypes,
			)
		}
	}

	validator, err := stakingtypes.NewValidator(msg.ValidatorAddress, pk, stakingtypes.Description{
		Moniker:         msg.Description.Moniker,
		Identity:        msg.Description.Identity,
		Website:         msg.Description.Website,
		SecurityContact: msg.Description.SecurityContact,
		Details:         msg.Description.Details,
	})
	if err != nil {
		return nil, err
	}

	commission := stakingtypes.NewCommissionWithTime(
		msg.Commission.Rate, msg.Commission.MaxRate,
		msg.Commission.MaxChangeRate, sdkCtx.BlockHeader().Time,
	)

	validator, err = validator.SetInitialCommission(commission)
	if err != nil {
		return nil, err
	}

	validator.MinSelfDelegation = sdkmath.NewInt(1)

	// appends the validator to a queue to wait for approval from an admin.
	if err := ms.k.AddPendingValidator(ctx, validator, pk); err != nil {
		return nil, err
	}

	return &poa.MsgCreateValidatorResponse{}, nil
}

func (ms msgServer) UpdateParams(ctx context.Context, msg *poa.MsgUpdateParams) (*poa.MsgUpdateParamsResponse, error) {
	if ok := ms.isAdmin(ctx, msg.Sender); !ok {
		return nil, poa.ErrNotAnAuthority
	}

	return &poa.MsgUpdateParamsResponse{}, ms.k.SetParams(ctx, msg.Params)
}

// UpdateStakingParams implements poa.MsgServer.
func (ms msgServer) UpdateStakingParams(ctx context.Context, msg *poa.MsgUpdateStakingParams) (*poa.MsgUpdateStakingParamsResponse, error) {
	if ok := ms.isAdmin(ctx, msg.Sender); !ok {
		return nil, poa.ErrNotAnAuthority
	}

	stakingParams := stakingtypes.Params{
		UnbondingTime:     msg.Params.UnbondingTime,
		MaxValidators:     msg.Params.MaxValidators,
		MaxEntries:        msg.Params.MaxEntries,
		HistoricalEntries: msg.Params.HistoricalEntries,
		BondDenom:         msg.Params.BondDenom,
		MinCommissionRate: msg.Params.MinCommissionRate,
	}

	return &poa.MsgUpdateStakingParamsResponse{}, ms.k.stakingKeeper.SetParams(ctx, stakingParams)
}

// takes in a validator address & sees if they are pending approval.
func (ms msgServer) acceptNewValidator(ctx context.Context, operatingAddress string, power uint64) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Get the validator configuration from their CreateValidator message in the past.
	poaVal, err := ms.k.GetPendingValidator(ctx, operatingAddress)
	if err != nil {
		return err
	}

	val := poa.ConvertPOAToStaking(poaVal)

	valAddr, err := ms.k.validatorAddressCodec.StringToBytes(val.OperatorAddress)
	if err != nil {
		return sdkerrors.ErrInvalidAddress.Wrapf("invalid validator address: %s", err)
	}

	err = ms.k.stakingKeeper.SetValidator(ctx, val)
	if err != nil {
		return err
	}

	err = ms.k.stakingKeeper.SetValidatorByConsAddr(ctx, val)
	if err != nil {
		return err
	}

	err = ms.k.stakingKeeper.SetNewValidatorByPowerIndex(ctx, val)
	if err != nil {
		return err
	}

	// sets validator slashing defaults (useful for downtime jailing)
	cons, err := val.GetConsAddr()
	if err != nil {
		return err
	}
	if err := ms.k.slashKeeper.SetValidatorSigningInfo(ctx, sdk.ConsAddress(cons), slashingtypes.ValidatorSigningInfo{
		Address:             sdk.ConsAddress(cons).String(),
		StartHeight:         sdkCtx.BlockHeight(),
		IndexOffset:         0,
		JailedUntil:         sdkCtx.BlockHeader().Time,
		Tombstoned:          false,
		MissedBlocksCounter: 0,
	}); err != nil {
		return err
	}

	if err := ms.k.stakingKeeper.Hooks().AfterValidatorCreated(ctx, valAddr); err != nil {
		return err
	}

	if err := ms.k.RemovePendingValidator(ctx, val.OperatorAddress); err != nil {
		return err
	}

	// The validator is actually created now, so emit the necessary events
	sdkCtx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			stakingtypes.EventTypeCreateValidator,
			sdk.NewAttribute(stakingtypes.AttributeKeyValidator, val.OperatorAddress),
			sdk.NewAttribute(sdk.AttributeKeyAmount, fmt.Sprintf("%d", power)),
		),
	})

	return nil
}

// updatePOAPower removes all delegations, sets a single delegation for POA power, updates the validator with the new shares
// and sets the last validator power to the new value.
func (ms msgServer) updatePOAPower(ctx context.Context, valOpBech32 string, power int64) (stakingtypes.Validator, error) {
	valAddr, err := sdk.ValAddressFromBech32(valOpBech32)
	if err != nil {
		return stakingtypes.Validator{}, err
	}

	val, err := ms.k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return stakingtypes.Validator{}, err
	}

	// remove all delegations (for safety)
	delegations, err := ms.k.stakingKeeper.GetValidatorDelegations(ctx, valAddr)
	if err != nil {
		return stakingtypes.Validator{}, err
	}

	for _, del := range delegations {
		if err := ms.k.stakingKeeper.RemoveDelegation(ctx, del); err != nil {
			return stakingtypes.Validator{}, err
		}
	}

	// set a single updated delegation of power
	delegation := stakingtypes.Delegation{
		DelegatorAddress: sdk.AccAddress(valAddr.Bytes()).String(),
		ValidatorAddress: val.OperatorAddress,
		Shares:           sdkmath.LegacyNewDec(power),
	}

	val.Tokens = sdkmath.NewIntFromUint64(uint64(power))
	val.DelegatorShares = delegation.Shares
	val.Status = stakingtypes.Bonded
	if err := ms.k.stakingKeeper.SetValidator(ctx, val); err != nil {
		return stakingtypes.Validator{}, err
	}

	if err := ms.k.stakingKeeper.SetDelegation(ctx, delegation); err != nil {
		return stakingtypes.Validator{}, err
	}

	if err := ms.k.stakingKeeper.SetLastValidatorPower(ctx, valAddr, power); err != nil {
		return stakingtypes.Validator{}, err
	}

	return val, nil
}

func (ms msgServer) isAdmin(ctx context.Context, fromAddr string) bool {
	for _, auth := range ms.k.GetAdmins(ctx) {
		if auth == fromAddr {
			return true
		}
	}

	return false
}
