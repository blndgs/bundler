package utils

import (
	"math/big"

	"github.com/blndgs/model"
	"github.com/ethereum/go-ethereum/common"
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

