package srv

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/utils"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
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

			inputData, err := s.parsedABI.Pack("simulateHandleOp", item, common.HexToAddress(s.sender), []byte{})
			if err != nil {
				log.Fatalf("Failed to pack input data: %v", err)
			}

			// Simulate transaction
			msg := ethereum.CallMsg{
				From: common.HexToAddress(s.sender),
				To:   &s.entryPointAddr,
				Data: inputData,
			}

			// Execute eth_call
			result, err := s.chainClient.CallContract(context.Background(), msg, nil)
			if err != nil {
				if err.Error() != "execution reverted" {
					log.Fatalf("Failed to execute eth_call: %v", err)
				}
				// Extract revert reason to parse the simulation result
				fmt.Println("Simulation reverted as expected. Extracting gas usage...")
			}

			// Decode revert result (execution reverts with gas usage details)
			type ExecutionResult struct {
				PreOpGas   *big.Int
				Paid       *big.Int
				ValidAfter *big.Int
				ValidUntil *big.Int
				Success    bool
				Result     []byte
			}

			var execResult ExecutionResult
			err = s.parsedABI.UnpackIntoInterface(&execResult, "simulateHandleOp", result)
			if err != nil {
				log.Fatalf("Failed to decode simulation result: %v", err)
			}

			json.NewEncoder(os.Stdout).Encode(execResult)
			fmt.Printf("Gas Used: %s\n", execResult.Paid.String()) // Since gasPrice = 1, Paid = GasUsed

			log.Println("Simulation succeeded without reversion (unexpected).")
			return nil
		}

		return nil
	}
}

// extractRevertData parses the revert reason from an error
func extractRevertData(err error) ([]byte, error) {
	// Check if the error contains the revert data
	if strings.Contains(err.Error(), "execution reverted") {
		// Extract the hex-encoded revert data
		start := strings.Index(err.Error(), "0x")
		if start == -1 {
			return nil, fmt.Errorf("no revert data found in error: %v", err)
		}
		revertDataHex := err.Error()[start:]
		revertData, decodeErr := hex.DecodeString(revertDataHex[2:]) // Remove "0x" prefix
		if decodeErr != nil {
			return nil, fmt.Errorf("failed to decode revert data: %v", decodeErr)
		}
		return revertData, nil
	}
	return nil, fmt.Errorf("error does not contain revert data: %v", err)
}
