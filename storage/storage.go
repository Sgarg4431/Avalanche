package storage

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/ids"
	smath "github.com/ava-labs/avalanchego/utils/math"
	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/consts"
	"github.com/ava-labs/hypersdk/crypto"

	"github.com/rafael-abuawad/samplevm/utils"
)

type ReadState func(context.Context, [][]byte) ([][]byte, []error)

// Metadata
// 0x0/ (tx)
//   -> [txID] => timestamp
//
// State
// 0x0/ (balance)
//   -> [owner|asset] => balance
// 0x1/ (assets)
//   -> [asset] => metadataLen|metadata|supply|owner|warp
// 0x2/ (hypersdk-incoming warp)
// 0x3/ (hypersdk-outgoing warp)

const (
	txPrefix = 0x0

	balancePrefix      = 0x0
	assetPrefix        = 0x1
	incomingWarpPrefix = 0x2
	outgoingWarpPrefix = 0x3
)

var (
	failureByte = byte(0x0)
	successByte = byte(0x1)
)

// [txPrefix] + [txID]
func PrefixTxKey(id ids.ID) (k []byte) {
	// TODO: use packer?
	k = make([]byte, 1+consts.IDLen)
	k[0] = txPrefix
	copy(k[1:], id[:])
	return
}

func StoreTransaction(
	_ context.Context,
	db database.KeyValueWriter,
	id ids.ID,
	t int64,
	success bool,
	units uint64,
) error {
	k := PrefixTxKey(id)
	v := make([]byte, consts.Uint64Len+1+consts.Uint64Len)
	binary.BigEndian.PutUint64(v, uint64(t))
	if success {
		v[consts.Uint64Len] = successByte
	} else {
		v[consts.Uint64Len] = failureByte
	}
	binary.BigEndian.PutUint64(v[consts.Uint64Len+1:], units)
	return db.Put(k, v)
}

func GetTransaction(
	_ context.Context,
	db database.KeyValueReader,
	id ids.ID,
) (bool, int64, bool, uint64, error) {
	k := PrefixTxKey(id)
	v, err := db.Get(k)
	if errors.Is(err, database.ErrNotFound) {
		return false, 0, false, 0, nil
	}
	if err != nil {
		return false, 0, false, 0, err
	}
	t := int64(binary.BigEndian.Uint64(v))
	success := true
	if v[consts.Uint64Len] == failureByte {
		success = false
	}
	units := binary.BigEndian.Uint64(v[consts.Uint64Len+1:])
	return true, t, success, units, nil
}

// [accountPrefix] + [address] + [asset]
func PrefixBalanceKey(pk crypto.PublicKey, asset ids.ID) (k []byte) {
	k = make([]byte, 1+crypto.PublicKeyLen+consts.IDLen)
	k[0] = balancePrefix
	copy(k[1:], pk[:])
	copy(k[1+crypto.PublicKeyLen:], asset[:])
	return
}

// If locked is 0, then account does not exist
func GetBalance(
	ctx context.Context,
	db chain.Database,
	pk crypto.PublicKey,
	asset ids.ID,
) (uint64, error) {
	k := PrefixBalanceKey(pk, asset)
	return innerGetBalance(db.GetValue(ctx, k))
}

// Used to serve RPC queries
func GetBalanceFromState(
	ctx context.Context,
	f ReadState,
	pk crypto.PublicKey,
	asset ids.ID,
) (uint64, error) {
	values, errs := f(ctx, [][]byte{PrefixBalanceKey(pk, asset)})
	return innerGetBalance(values[0], errs[0])
}

func innerGetBalance(
	v []byte,
	err error,
) (uint64, error) {
	if errors.Is(err, database.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(v), nil
}

func SetBalance(
	ctx context.Context,
	db chain.Database,
	pk crypto.PublicKey,
	asset ids.ID,
	balance uint64,
) error {
	k := PrefixBalanceKey(pk, asset)
	b := binary.BigEndian.AppendUint64(nil, balance)
	return db.Insert(ctx, k, b)
}

func DeleteBalance(
	ctx context.Context,
	db chain.Database,
	pk crypto.PublicKey,
	asset ids.ID,
) error {
	return db.Remove(ctx, PrefixBalanceKey(pk, asset))
}

func AddBalance(
	ctx context.Context,
	db chain.Database,
	pk crypto.PublicKey,
	asset ids.ID,
	amount uint64,
) error {
	bal, err := GetBalance(ctx, db, pk, asset)
	if err != nil {
		return err
	}
	nbal, err := smath.Add64(bal, amount)
	if err != nil {
		return fmt.Errorf(
			"%w: could not add balance (asset=%s, bal=%d, addr=%v, amount=%d)",
			ErrInvalidBalance,
			asset,
			bal,
			utils.Address(pk),
			amount,
		)
	}
	return SetBalance(ctx, db, pk, asset, nbal)
}

func SubBalance(
	ctx context.Context,
	db chain.Database,
	pk crypto.PublicKey,
	asset ids.ID,
	amount uint64,
) error {
	bal, err := GetBalance(ctx, db, pk, asset)
	if err != nil {
		return err
	}
	nbal, err := smath.Sub(bal, amount)
	if err != nil {
		return fmt.Errorf(
			"%w: could not subtract balance (asset=%s, bal=%d, addr=%v, amount=%d)",
			ErrInvalidBalance,
			asset,
			bal,
			utils.Address(pk),
			amount,
		)
	}
	if nbal == 0 {
		// If there is no balance left, we should delete the record instead of
		// setting it to 0.
		return db.Remove(ctx, PrefixBalanceKey(pk, asset))
	}
	return SetBalance(ctx, db, pk, asset, nbal)
}

// [assetPrefix] + [address]
func PrefixAssetKey(asset ids.ID) (k []byte) {
	k = make([]byte, 1+consts.IDLen)
	k[0] = assetPrefix
	copy(k[1:], asset[:])
	return
}

// Used to serve RPC queries
func GetAssetFromState(
	ctx context.Context,
	f ReadState,
	asset ids.ID,
) (bool, []byte, uint64, crypto.PublicKey, bool, error) {
	values, errs := f(ctx, [][]byte{PrefixAssetKey(asset)})
	return innerGetAsset(values[0], errs[0])
}

func GetAsset(
	ctx context.Context,
	db chain.Database,
	asset ids.ID,
) (bool, []byte, uint64, crypto.PublicKey, bool, error) {
	k := PrefixAssetKey(asset)
	return innerGetAsset(db.GetValue(ctx, k))
}

func innerGetAsset(
	v []byte,
	err error,
) (bool, []byte, uint64, crypto.PublicKey, bool, error) {
	if errors.Is(err, database.ErrNotFound) {
		return false, nil, 0, crypto.EmptyPublicKey, false, nil
	}
	if err != nil {
		return false, nil, 0, crypto.EmptyPublicKey, false, err
	}
	metadataLen := binary.BigEndian.Uint16(v)
	metadata := v[consts.Uint16Len : consts.Uint16Len+metadataLen]
	supply := binary.BigEndian.Uint64(v[consts.Uint16Len+metadataLen:])
	var pk crypto.PublicKey
	copy(pk[:], v[consts.Uint16Len+metadataLen+consts.Uint64Len:])
	warp := v[consts.Uint16Len+metadataLen+consts.Uint64Len+crypto.PublicKeyLen] == 0x1
	return true, metadata, supply, pk, warp, nil
}

func SetAsset(
	ctx context.Context,
	db chain.Database,
	asset ids.ID,
	metadata []byte,
	supply uint64,
	owner crypto.PublicKey,
	warp bool,
) error {
	k := PrefixAssetKey(asset)
	metadataLen := len(metadata)
	v := make([]byte, consts.Uint16Len+metadataLen+consts.Uint64Len+crypto.PublicKeyLen+1)
	binary.BigEndian.PutUint16(v, uint16(metadataLen))
	copy(v[consts.Uint16Len:], metadata)
	binary.BigEndian.PutUint64(v[consts.Uint16Len+metadataLen:], supply)
	copy(v[consts.Uint16Len+metadataLen+consts.Uint64Len:], owner[:])
	b := byte(0x0)
	if warp {
		b = 0x1
	}
	v[consts.Uint16Len+metadataLen+consts.Uint64Len+crypto.PublicKeyLen] = b
	return db.Insert(ctx, k, v)
}

func DeleteAsset(ctx context.Context, db chain.Database, asset ids.ID) error {
	k := PrefixAssetKey(asset)
	return db.Remove(ctx, k)
}

func IncomingWarpKeyPrefix(sourceChainID ids.ID, msgID ids.ID) (k []byte) {
	k = make([]byte, 1+consts.IDLen*2)
	k[0] = incomingWarpPrefix
	copy(k[1:], sourceChainID[:])
	copy(k[1+consts.IDLen:], msgID[:])
	return k
}

func OutgoingWarpKeyPrefix(txID ids.ID) (k []byte) {
	k = make([]byte, 1+consts.IDLen)
	k[0] = outgoingWarpPrefix
	copy(k[1:], txID[:])
	return k
}
