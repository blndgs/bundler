package srv

import (
	"math/big"
	"time"

	"github.com/blndgs/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/go-logr/logr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint/transaction"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/signer"
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
			est, revert, err := transaction.EstimateHandleOpsGas(&opts)

			if err != nil {
				return err
			} else if revert != nil {
				ctx.MarkOpIndexForRemoval(revert.OpIndex, revert.Reason)
			} else {
				opts.GasLimit = est
				break
			}
		}

		// Call handleOps() with gas estimate. Any userOps that cause a revert at this stage will be
		// caught and dropped in the next iteration.
		if len(ctx.Batch) > 0 {
			if txn, err := transaction.HandleOps(&opts); err != nil {
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
