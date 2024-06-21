package validations

import (
	"context"
	"math/big"

	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/go-logr/logr"
	"github.com/stackup-wallet/stackup-bundler/pkg/altmempools"
	"github.com/stackup-wallet/stackup-bundler/pkg/errors"
	"github.com/stackup-wallet/stackup-bundler/pkg/gas"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/checks"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/entities"
	"golang.org/x/sync/errgroup"
)

// Validator exposes modules to perform basic Client and Bundler checks as specified in EIP-4337. It is
// intended for bundlers that are independent of an Ethereum node and hence relies on a given ethClient to
// query blockchain state.
type Validator struct {
	db                 *badger.DB
	rpc                *rpc.Client
	eth                *ethclient.Client
	ov                 *gas.Overhead
	alt                *altmempools.Directory
	maxVerificationGas *big.Int
	maxBatchGasLimit   *big.Int
	isRIP7212Supported bool
	tracer             string
	repConst           *entities.ReputationConstants
	logger             logr.Logger
}

// New returns a Standalone instance with methods that can be used in Client and Bundler modules to perform
// standard checks as specified in EIP-4337.
func New(
	db *badger.DB,
	rpc *rpc.Client,
	ov *gas.Overhead,
	alt *altmempools.Directory,
	maxVerificationGas *big.Int,
	maxBatchGasLimit *big.Int,
	isRIP7212Supported bool,
	tracer string,
	repConst *entities.ReputationConstants,
	logger logr.Logger,
) *Validator {
	eth := ethclient.NewClient(rpc)
	return &Validator{
		db,
		rpc,
		eth,
		ov,
		alt,
		maxVerificationGas,
		maxBatchGasLimit,
		isRIP7212Supported,
		tracer,
		repConst,
		logger,
	}
}

// getCodeWithEthClient returns a GetCodeFunc that uses an eth client to call eth_getCode.
func getCodeWithEthClient(eth *ethclient.Client) checks.GetCodeFunc {
	return func(addr common.Address) ([]byte, error) {
		return eth.CodeAt(context.Background(), addr, nil)
	}
}

// OpValues returns a UserOpHandler that runs through some first line sanity checks for new UserOps
// received by the Client. This should be one of the first modules executed by the Client.
func (v *Validator) OpValues() modules.UserOpHandlerFunc {
	return func(ctx *modules.UserOpHandlerCtx) error {
		gc := getCodeWithEthClient(v.eth)

		g := new(errgroup.Group)
		g.Go(func() error { return checks.ValidateSender(ctx.UserOp, gc) })
		g.Go(func() error { return checks.ValidateInitCode(ctx.UserOp) })
		g.Go(func() error { return checks.ValidateVerificationGas(ctx.UserOp, v.ov, v.maxVerificationGas) })
		g.Go(func() error { return checks.ValidatePaymasterAndData(ctx.UserOp, ctx.GetPaymasterDepositInfo(), gc) })
		g.Go(func() error { return checks.ValidateCallGasLimit(ctx.UserOp, v.ov) })
		// g.Go(func() error { return checks.ValidateFeePerGas(ctx.UserOp, gasprice.GetBaseFeeWithEthClient(v.eth)) })
		g.Go(func() error { return checks.ValidatePendingOps(ctx.UserOp, ctx.GetPendingSenderOps()) })
		g.Go(func() error { return checks.ValidateGasAvailable(ctx.UserOp, v.maxBatchGasLimit) })

		if err := g.Wait(); err != nil {
			v.logger.Error(err, "could not fetch user operation values")
			return errors.NewRPCError(errors.INVALID_FIELDS, err.Error(), err.Error())
		}

		return nil
	}
}

func (v *Validator) ToStandaloneCheck() *checks.Standalone {
	return checks.New(v.db, v.rpc, v.ov, v.alt, v.maxVerificationGas,
		v.maxBatchGasLimit, v.isRIP7212Supported, v.tracer, v.repConst)
}
