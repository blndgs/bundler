package srv

import (
	"context"
	"encoding/hex"
	"math"
	"math/big"
	"net/http"
	"strings"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/utils"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/go-logr/logr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/signer"
)

type Simulator struct {
	cfg            *conf.Values
	chainClient    *ethclient.Client
	logger         logr.Logger
	txHashes       *xsync.MapOf[string, OpHashes]
	entryPointAddr common.Address
	chainID        *big.Int
	sender         string
	httpClient     *http.Client

	parsedABI abi.ABI
}

func NewSimulator(
	signer *signer.EOA,
	cfg *conf.Values,
	chainClient *ethclient.Client,
	logger logr.Logger, txHashes *xsync.MapOf[string, OpHashes],
	entrypointAddr common.Address, chainID *big.Int) (*Simulator, error) {

	client := &http.Client{
		Timeout: cfg.SimulationTimeout,
	}

	parsed, err := abi.JSON(strings.NewReader(entrypoint.EntrypointABI))
	if err != nil {
		return nil, err
	}

	return &Simulator{
		cfg:            cfg,
		sender:         signer.Address.Hex(),
		chainClient:    chainClient,
		logger:         logger,
		txHashes:       txHashes,
		entryPointAddr: entrypointAddr,
		chainID:        chainID,
		httpClient:     client,
		parsedABI:      parsed,
	}, nil
}

func (s *Simulator) Onchain(sender *signer.EOA) modules.BatchHandlerFunc {
	return func(batch *modules.BatchHandlerCtx) error {

		logger := s.logger

		if !s.cfg.SimulationEnabled {
			logger.Info("skipping simulation")
			return nil
		}

		ctx, span := utils.GetTracer().
			Start(context.Background(), "simulator.onchain")
		defer span.End()

		_ = ctx

		for _, item := range batch.Batch {

			d, _ := hex.DecodeString("0x")

			ep, err := entrypoint.NewEntrypoint(s.entryPointAddr, s.chainClient)
			if err != nil {
				return err
			}
			gasPrice, err := s.chainClient.SuggestGasPrice(ctx)
			if err != nil {
				return err
			}

			auth, err := bind.NewKeyedTransactorWithChainID(sender.PrivateKey, s.chainID)
			if err != nil {
				return err
			}

			auth.GasLimit = math.MaxUint64
			auth.NoSend = false

			tx, err := ep.SimulateHandleOp(auth, entrypoint.UserOperation(*item),
				common.HexToAddress(s.cfg.Beneficiary), d)
			if err != nil {
				logger.Error(err, "could not simulate")
				return err
			}

			callMsg := ethereum.CallMsg{
				To:         tx.To(),
				Gas:        gasPrice.Uint64(),
				GasFeeCap:  tx.GasFeeCap(),
				GasTipCap:  tx.GasTipCap(),
				Value:      tx.Value(),
				Data:       tx.Data(),
				AccessList: tx.AccessList(),
				From:       sender.Address,
			}

			_, err = s.chainClient.CallContract(ctx, callMsg, nil)
			if err != nil {
				logger.Error(err, "could not call contract")
				return err
			}

		}

		return nil
	}
}
