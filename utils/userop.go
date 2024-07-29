package utils

import (
	"math/big"

	"github.com/blndgs/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
)

// GetUserOpHash returns the hash for the userop
func GetUserOpHash(op *userop.UserOperation,
	entrypointAddress common.Address,
	chainID *big.Int) (currentOpHash string, unsolvedOpHash string) {

	currentOpHash = op.GetUserOpHash(entrypointAddress, chainID).String()

	unsolvedOpHash = currentOpHash
	mOp := (model.UserOperation)(*op)
	if mOp.HasIntent() && len(mOp.Signature) > model.KernelSignatureLength {
		// Restore the userOp to unsolved state
		mOp.CallData = mOp.Signature[mOp.GetSignatureEndIdx():]
		mOp.Signature = mOp.GetSignatureValue()

		unsolvedOpHash = mOp.GetUserOpHash(entrypointAddress, chainID).String()
	}

	return
}

// DropAllUserOps will drop the entire userops in the current batch and add it to the pending removal array.
// These userops will be dropped from the mempool and won't go on-chain either
func DropAllUserOps(c *modules.BatchHandlerCtx, reason string) {
	for _, op := range c.Batch {
		c.PendingRemoval = append(c.PendingRemoval, &modules.PendingRemovalItem{
			Op:     op,
			Reason: reason,
		})
	}

	c.Batch = []*userop.UserOperation{}
}
