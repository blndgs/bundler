package srv

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/blndgs/bundler/utils"
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
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const (
	DefaultWaitTimeout = 72 * time.Second
)

type OpHashes struct {
	Solved string
	Trx    common.Hash

	// If this encountered an error earlier
	Error error
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

func (r *Relayer) computeOPHashes(
	userOps []*userop.UserOperation,
	err error,
	hash common.Hash) {

	for _, op := range userOps {

		currentOpHash, unsolvedOpHash := utils.GetUserOpHash(op, r.ep, r.chainID)

		r.m.Compute(unsolvedOpHash,
			func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {

				return OpHashes{
					Error:  errors.Join(oldValue.Error, err),
					Solved: currentOpHash,
					Trx:    hash,
				}, false
			})
	}
}

// SendUserOperation returns a BatchHandler that is used by the Bundler to send batches in a regular EOA
// transaction.
func (r *Relayer) SendUserOperation() modules.BatchHandlerFunc {
	return func(ctx *modules.BatchHandlerCtx) error {

		_, span := utils.GetTracer().
			Start(context.Background(), "SendUserOperation")
		defer span.End()

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

		span.SetAttributes(
			attribute.String("gas_price", ctx.GasPrice.String()),
			attribute.String("beneficiary", r.beneficiary.Hex()),
			attribute.Int64("chain_id", ctx.ChainID.Int64()),
			attribute.Int64("wait_timeout", int64(r.waitTimeout)),
		)

		// Estimate gas for handleOps() and drop all userOps that cause unexpected reverts.
		for len(ctx.Batch) > 0 {
			est, revert, err := estimateHandleOpsGas(&opts)
			if revert != nil {
				r.computeOPHashes(ctx.Batch, errors.New(revert.Reason), common.Hash{})
				ctx.MarkOpIndexForRemoval(revert.OpIndex, revert.Reason)
			} else if err != nil {
				r.logger.Error(err, "failed to estimate gas for handleOps")

				err = fmt.Errorf("failed to estimate gas for handleOps likely not enough gas: %w", err)
				r.computeOPHashes(ctx.Batch, err, common.Hash{})
				ctx.MarkOpIndexForRemoval(0, err.Error())

				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
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

				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				r.computeOPHashes(ctx.Batch, err, common.Hash{})
				return err
			} else {
				txHash := txn.Hash().String()
				ctx.Data["txn_hash"] = txHash
				r.computeOPHashes(ctx.Batch, nil, txn.Hash())
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
