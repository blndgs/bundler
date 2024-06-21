package srv

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/blndgs/model"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/go-logr/logr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint/reverts"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint/transaction"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/signer"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
)

const (
	DefaultWaitTimeout = 72 * time.Second
)

type OpHashes struct {
	Solved string
	Trx    common.Hash
}

// Relayer provides a module that can relay batches with a regular EOA. Relaying batches to the EntryPoint
// through a regular transaction comes with several important notes:
//
//   - The bundler will NOT be operating as a block builder.
//   - This opens the bundler up to frontrunning.
//
// This module only works in the case of a private mempool and will not work in the P2P case where ops are
// propagated through the network and it is impossible to prevent collisions from multiple bundlers trying to
// relay the same ops.
type Relayer struct {
	m           *xsync.MapOf[string, OpHashes]
	ep          common.Address
	eoa         *signer.EOA
	eth         *ethclient.Client
	chainID     *big.Int
	beneficiary common.Address
	logger      logr.Logger
	waitTimeout time.Duration
}

// New initializes a new EOA relayer for sending batches to the EntryPoint.
func New(
	ep common.Address,
	eoa *signer.EOA,
	eth *ethclient.Client,
	chainID *big.Int,
	beneficiary common.Address,
	l logr.Logger,
) *Relayer {
	return &Relayer{
		m:           xsync.NewMapOf[string, OpHashes](),
		ep:          ep,
		eoa:         eoa,
		eth:         eth,
		chainID:     chainID,
		beneficiary: beneficiary,
		logger:      l.WithName("relayer"),
		waitTimeout: DefaultWaitTimeout,
	}
}

func (r *Relayer) GetOpHashes() *xsync.MapOf[string, OpHashes] {
	return r.m
}

// SetWaitTimeout sets the total time to wait for a transaction to be included. When a timeout is reached, the
// BatchHandler will throw an error if the transaction has not been included or has been included but with a
// failed status.
//
// The default value is 30 seconds. Setting the value to 0 will skip waiting for a transaction to be included.
func (r *Relayer) SetWaitTimeout(timeout time.Duration) {
	r.waitTimeout = timeout
}

// SendUserOperation returns a BatchHandler that is used by the Bundler to send batches in a regular EOA
// transaction.
func (r *Relayer) SendUserOperation() modules.BatchHandlerFunc {
	return func(ctx *modules.BatchHandlerCtx) error {
		opts := transaction.Opts{
			EOA:         r.eoa,
			Eth:         r.eth,
			ChainID:     ctx.ChainID,
			EntryPoint:  ctx.EntryPoint,
			Batch:       ctx.Batch,
			Beneficiary: r.beneficiary,
			BaseFee:     ctx.BaseFee,
			Tip:         ctx.Tip,
			GasPrice:    ctx.GasPrice,
			GasLimit:    0,
			WaitTimeout: r.waitTimeout,
		}
		// Estimate gas for handleOps() and drop all userOps that cause unexpected reverts.
		for len(ctx.Batch) > 0 {
			est, revert, err := estimateHandleOpsGas(&opts)
			if revert != nil {
				ctx.MarkOpIndexForRemoval(revert.OpIndex, revert.Reason)
			} else if err != nil {
				r.logger.Error(err, "failed to estimate gas for handleOps")

				err = fmt.Errorf("failed to estimate gas for handleOps likely not enough gas: %w", err)
				ctx.MarkOpIndexForRemoval(0, err.Error())
			} else {
				opts.GasLimit = est
				opts.GasPrice = ctx.GasPrice
				break
			}
		}

		// Call handleOps() with gas estimate. Any userOps that cause a revert at this stage will be
		// caught and dropped in the next iteration.
		if len(ctx.Batch) > 0 {
			if txn, err := transaction.HandleOps(&opts); err != nil {
				r.logger.Error(err, "user ops could not be sent onchain")
				return err
			} else {
				txHash := txn.Hash().String()
				ctx.Data["txn_hash"] = txHash
				// Store the transaction hash for each userOp in the batch.
				for _, op := range ctx.Batch {
					currentOpHash := op.GetUserOpHash(r.ep, r.chainID).String()
					opHashes := OpHashes{
						Solved: currentOpHash,
						Trx:    txn.Hash(),
					}

					mOp := (model.UserOperation)(*op)
					var unsolvedOpHash = currentOpHash
					if mOp.HasIntent() && len(mOp.Signature) > model.SignatureLength {
						// Restore the userOp to unsolved state
						mOp.CallData = mOp.Signature[model.SignatureLength:]
						mOp.Signature = mOp.Signature[:model.SignatureLength]

						unsolvedOpHash = mOp.GetUserOpHash(r.ep, r.chainID).String()
					}
					r.m.Store(unsolvedOpHash, opHashes)
				}
			}
		}

		return nil
	}
}

// estimateHandleOpsGas returns a gas estimate required to call handleOps() with a given batch. A failed call
// will return the cause of the revert.
// Copied from stackup to keep the gas price from the batch context and avoid a negative gas result.
func estimateHandleOpsGas(opts *transaction.Opts) (gas uint64, revert *reverts.FailedOpRevert, err error) {
	castToEPType := func(batch []*userop.UserOperation) []entrypoint.UserOperation {
		length := len(batch)
		ops := make([]entrypoint.UserOperation, length)
		for i := 0; i < length; i++ {
			ops[i] = entrypoint.UserOperation(*batch[i])
		}

		return ops
	}

	ep, err := entrypoint.NewEntrypoint(opts.EntryPoint, opts.Eth)
	if err != nil {
		return 0, nil, err
	}

	auth, err := bind.NewKeyedTransactorWithChainID(opts.EOA.PrivateKey, opts.ChainID)
	if err != nil {
		return 0, nil, err
	}
	auth.GasLimit = math.MaxUint64
	auth.NoSend = true

	tx, err := ep.HandleOps(auth, castToEPType(opts.Batch), opts.Beneficiary)
	if err != nil {
		return 0, nil, err
	}

	est, err := opts.Eth.EstimateGas(context.Background(), ethereum.CallMsg{
		From: opts.EOA.Address,
		To:   tx.To(),
		Gas:  tx.Gas(),
		// GasPrice:   opts.GasPrice, Using the gas price from tx results in negative gas estimation.
		GasFeeCap:  tx.GasFeeCap(),
		GasTipCap:  tx.GasTipCap(),
		Value:      tx.Value(),
		Data:       tx.Data(),
		AccessList: tx.AccessList(),
	})
	if err != nil {
		revert, err := reverts.NewFailedOp(err)
		if err != nil {
			return 0, nil, err
		}
		return 0, revert, nil
	}

	return est, nil, nil
}
