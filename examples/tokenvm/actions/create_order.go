// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package actions

import (
	"context"
	"fmt"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/consts"
	"github.com/ava-labs/hypersdk/examples/tokenvm/auth"
	"github.com/ava-labs/hypersdk/examples/tokenvm/storage"
	"github.com/ava-labs/hypersdk/utils"
)

var _ chain.Action = (*CreateOrder)(nil)

type CreateOrder struct {
	// [In] is the asset you trade for [Out].
	In ids.ID `json:"in"`

	// [InRate] is the amount of [In] required to purchase
	// [OutRate] of [Out].
	InRate uint64 `json:"inRate"`

	// [Out] is the asset you receive when trading for [In].
	//
	// This is the asset that is actually provided by the creator.
	Out ids.ID `json:"out"`

	// [OutRate] is the amount of [Out] the counterparty gets per [InRate] of
	// [In].
	OutRate uint64 `json:"outRate"`

	// [Supply] is the initial amount of [In] that the actor is locking up.
	// TODO: ensure supply is a multiple of OutRate
	Supply uint64 `json:"supply"`

	// Notes:
	// * Users are allowed to have any number of orders for the same [In]-[Out] pair.
	// * Using [InRate] and [OutRate] blocks ensures we avoid any odd rounding
	//	 errors.
}

func (c *CreateOrder) StateKeys(rauth chain.Auth, txID ids.ID) [][]byte {
	actor := auth.GetActor(rauth)
	return [][]byte{
		storage.PrefixBalanceKey(actor, c.Out),
		storage.PrefixOrderKey(txID),
	}
}

func (c *CreateOrder) Execute(
	ctx context.Context,
	r chain.Rules,
	db chain.Database,
	_ int64,
	rauth chain.Auth,
	txID ids.ID,
) (*chain.Result, error) {
	actor := auth.GetActor(rauth)
	unitsUsed := c.MaxUnits(r) // max units == units
	if c.InRate == 0 {
		return &chain.Result{Success: false, Units: unitsUsed, Output: OutputInRateZero}, nil
	}
	if c.OutRate == 0 {
		return &chain.Result{Success: false, Units: unitsUsed, Output: OutputOutRateZero}, nil
	}
	if c.Supply == 0 {
		return &chain.Result{Success: false, Units: unitsUsed, Output: OutputSupplyZero}, nil
	}
	if c.Supply%c.OutRate != 0 {
		return &chain.Result{Success: false, Units: unitsUsed, Output: OutputSupplyMisaligned}, nil
	}
	if err := storage.SubBalance(ctx, db, actor, c.Out, c.Supply); err != nil {
		return &chain.Result{Success: false, Units: unitsUsed, Output: utils.ErrBytes(err)}, nil
	}
	if err := storage.SetOrder(ctx, db, txID, c.In, c.InRate, c.Out, c.OutRate, c.Supply, actor); err != nil {
		return &chain.Result{Success: false, Units: unitsUsed, Output: utils.ErrBytes(err)}, nil
	}
	return &chain.Result{Success: true, Units: unitsUsed}, nil
}

func (*CreateOrder) MaxUnits(chain.Rules) uint64 {
	// We use size as the price of this transaction but we could just as easily
	// use any other calculation.
	return consts.IDLen*2 + consts.Uint64Len*3
}

func (c *CreateOrder) Marshal(p *codec.Packer) {
	p.PackID(c.In)
	p.PackUint64(c.InRate)
	p.PackID(c.Out)
	p.PackUint64(c.OutRate)
	p.PackUint64(c.Supply)
}

func UnmarshalCreateOrder(p *codec.Packer) (chain.Action, error) {
	var create CreateOrder
	p.UnpackID(false, &create.In) // empty ID is the native asset
	create.InRate = p.UnpackUint64(true)
	p.UnpackID(false, &create.Out) // empty ID is the native asset
	create.OutRate = p.UnpackUint64(true)
	create.Supply = p.UnpackUint64(true)
	return &create, p.Err()
}

func (*CreateOrder) ValidRange(chain.Rules) (int64, int64) {
	// Returning -1, -1 means that the action is always valid.
	return -1, -1
}

func PairID(in ids.ID, out ids.ID) string {
	return fmt.Sprintf("%s-%s", in.String(), out.String())
}
