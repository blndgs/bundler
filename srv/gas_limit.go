package srv

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/utils"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint/transaction"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

func (r *Relayer) LimitGas(values *conf.Values) modules.BatchHandlerFunc {
	return func(ctx *modules.BatchHandlerCtx) error {

		if !values.EnableGasLimitChecks {
			return nil
		}

		_, span := utils.GetTracer().
			Start(context.Background(), "Relayer.LimitGas")
		defer span.End()

		span.SetAttributes(
			attribute.String("gas_price", ctx.GasPrice.String()),
			attribute.String("beneficiary", r.beneficiary.Hex()),
			attribute.Int64("chain_id", ctx.ChainID.Int64()),
			attribute.Int64("wait_timeout", int64(r.waitTimeout)),
		)

		// Estimate gas for handleOps() and drop all userOps that cause unexpected reverts.
		for idx, op := range ctx.Batch {

			opts := transaction.Opts{
				EOA:         r.eoa,
				Eth:         r.eth,
				ChainID:     ctx.ChainID,
				EntryPoint:  ctx.EntryPoint,
				Batch:       []*userop.UserOperation{op},
				Beneficiary: r.beneficiary,
				BaseFee:     ctx.BaseFee,
				Tip:         ctx.Tip,
				GasPrice:    ctx.GasPrice,
				GasLimit:    0,
				WaitTimeout: r.waitTimeout,
			}

			est, revert, err := estimateHandleOpsGas(&opts)
			if revert != nil {
				ctx.MarkOpIndexForRemoval(revert.OpIndex, revert.Reason)
			} else if err != nil {
				r.logger.Error(err, "failed to estimate gas for handleOps")

				err = fmt.Errorf("failed to estimate gas for handleOps likely not enough gas: %w", err)
				ctx.MarkOpIndexForRemoval(idx, err.Error())

				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			} else {

				if big.NewInt(0).SetUint64(est).Cmp(values.SingleUseropGaslimit) == 1 {

					err := errors.New("gas too high")

					ctx.MarkOpIndexForRemoval(idx, err.Error())

					currentOpHash, unsolvedOpHash := utils.GetUserOpHash(op, r.ep, r.chainID)

					r.m.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
						return OpHashes{
							Error:  multierror.Append(oldValue.Error, err),
							Solved: currentOpHash,
						}, false
					})

					r.logger.WithValues(
						"single_userop_gas_limit", values.SingleUseropGaslimit.Int64(),
						"current_userop_gas_limit", op.CallGasLimit.Int64()).
						Error(err, "gas too high for this transaction")

					span.RecordError(errors.New("gas too high"))
					span.SetStatus(codes.Error, err.Error())
				}
				break
			}
		}

		return nil
	}
}

// func (r *Relayer) estimateUserOpsGas(ctx context.Context,
// 	rpcClient *rpc.Client,
// 	op *userop.UserOperation,
// 	entryPoint common.Address) (*gas.GasEstimates, error) {
//
// 	var estimate gas.GasEstimates
//
// 	err := rpcClient.CallContext(ctx, &estimate, "eth_estimateUserOperationGas", op, entryPoint)
// 	return &estimate, err
// }
