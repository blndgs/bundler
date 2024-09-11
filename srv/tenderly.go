package srv

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/go-logr/logr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
	"golang.org/x/sync/errgroup"
)

func SimulateTxWithTenderly(cfg *conf.Values,
	chainCliet *ethclient.Client,
	logger logr.Logger, txHashes *xsync.MapOf[string, OpHashes],
	entrypointAddr common.Address, chainID *big.Int) modules.BatchHandlerFunc {

	client := &http.Client{
		Timeout: cfg.SimulationTimeout,
	}

	return func(ctx *modules.BatchHandlerCtx) error {

		if !cfg.SimulationEnabled {
			logger.Info("skipping simulation")
			return nil
		}

		_, span := utils.GetTracer().
			Start(context.Background(), "SimulateTxWithTenderly")
		defer span.End()

		var g errgroup.Group

		computeHashFn := func(unsolvedOpHash, currentOpHash string, err error) {
			txHashes.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
				return OpHashes{
					Error:  errors.Join(oldValue.Error, err),
					Solved: currentOpHash,
				}, false
			})
		}

		for idx, userop := range ctx.Batch {
			logger.Info(hex.EncodeToString(userop.CallData), "index", idx)

			g.Go(func() error {

				currentOpHash, unsolvedOpHash := utils.GetUserOpHash(userop, entrypointAddr, chainID)

				resp, err := doSimulateUserop(userop, logger, client, entrypointAddr, cfg)
				if err != nil {
					computeHashFn(unsolvedOpHash, currentOpHash, err)
					return err
				}

				// all supported usecases involve asset changes at this time
				// Swapping, staking, loan supply/withdrawing
				if len(resp.Result.AssetChanges) == 0 {
					err := errors.New("unexpected response: asset changes not detected")
					computeHashFn(unsolvedOpHash, currentOpHash, err)
					return err
				}

				// only check the last TX to make sure it is being deposited to the userop sender
				incomingAssetChanges := resp.Result.AssetChanges[len(resp.Result.AssetChanges)-1]

				if common.HexToAddress(incomingAssetChanges.To).Hex() != userop.Sender.Hex() {
					err = errors.New("final asset deposit transaction does not belong to the userop sender")
					logger.Error(err, "unexpected asset_changes structure")
					computeHashFn(unsolvedOpHash, currentOpHash, err)
					return err
				}

				return nil
			})
		}

		return g.Wait()
	}
}

func doSimulateUserop(userop *userop.UserOperation,
	logger logr.Logger,
	client *http.Client,
	entrypointAddr common.Address,
	cfg *conf.Values) (simulationResponse, error) {

	var data = simulationRequest{
		Data: "0x" + hex.EncodeToString(userop.CallData),
		To:   userop.Sender.Hex(),
		From: entrypointAddr.Hex(),
	}

	r := tenderlySimulationRequest{
		Params: []interface {
		}{data, "latest"},
		Method:  "tenderly_simulateTransaction",
		Jsonrpc: "2.0",
		Id:      1,
	}

	var b = new(bytes.Buffer)

	if err := json.NewEncoder(b).Encode(r); err != nil {
		return simulationResponse{}, err
	}

	req, err := http.NewRequest(http.MethodPost, cfg.EthClientUrl, b)
	if err != nil {
		return simulationResponse{}, err
	}

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		logger.Error(err, "could not make http request to Tenderly")

		err = errors.New("could not simulate transaction. error making http request")
		return simulationResponse{}, err
	}

	defer resp.Body.Close()

	if resp.StatusCode > http.StatusOK {
		var errResp struct {
			Error struct {
				ID      string `json:"id"`
				Slug    string `json:"slug"`
				Message string `json:"message"`
			} `json:"error"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			logger.Error(err, "could not decode simulation response")
			return simulationResponse{}, err
		}

		err := errors.New(errResp.Error.Message)

		logger.Error(err, "an error occured while making simulation request",
			"status_code", resp.StatusCode,
			"slug", errResp.Error.Slug,
			"error_id", errResp.Error.ID)

		return simulationResponse{}, err
	}

	var simulatedResponse simulationResponse

	if err := json.NewDecoder(resp.Body).Decode(&simulatedResponse); err != nil {
		logger.Error(err, "could not decode simulation response")
		return simulationResponse{}, err
	}

	if !simulatedResponse.Result.Status {
		// all failures have an "execution reverted" message
		logger.Error(errors.New("execution reverted"), "could not simulate transaction")

		err = errors.New("could not simulate transaction. execution reverted")

		return simulationResponse{}, err
	}

	return simulatedResponse, nil
}

type tenderlySimulationRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Id      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type simulationResponse struct {
	ID      int    `json:"id"`
	Jsonrpc string `json:"jsonrpc"`
	Result  struct {
		Status       bool `json:"status"`
		AssetChanges []struct {
			AssetInfo struct {
				Standard        string `json:"standard"`
				Type            string `json:"type"`
				ContractAddress string `json:"contractAddress"`
				Symbol          string `json:"symbol"`
				Name            string `json:"name"`
				Logo            string `json:"logo"`
				Decimals        int    `json:"decimals"`
				DollarValue     string `json:"dollarValue"`
			} `json:"assetInfo"`
			Type        string `json:"type"`
			From        string `json:"from"`
			To          string `json:"to"`
			RawAmount   string `json:"rawAmount"`
			Amount      string `json:"amount"`
			DollarValue string `json:"dollarValue"`
		} `json:"assetChanges"`
		BalanceChanges []struct {
			Address     string `json:"address"`
			DollarValue string `json:"dollarValue"`
			Transfers   []int  `json:"transfers"`
		} `json:"balanceChanges"`
	} `json:"result"`
}

type simulationRequest struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Data string `json:"data,omitempty"`
}
