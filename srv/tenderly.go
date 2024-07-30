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
	multierror "github.com/hashicorp/go-multierror"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"golang.org/x/sync/errgroup"
)

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

		for idx, userop := range ctx.Batch {
			logger.Info(hex.EncodeToString(userop.CallData), "index", idx)

			currentOpHash, unsolvedOpHash := utils.GetUserOpHash(userop, entrypointAddr, chainID)

			var data = struct {
				From     string `json:"from,omitempty"`
				To       string `json:"to,omitempty"`
				Gas      string `json:"gas,omitempty"`
				GasPrice string `json:"gasPrice,omitempty"`
				Value    string `json:"value,omitempty"`
				Data     string `json:"data,omitempty"`
			}{
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
				return err
			}

			req, err := http.NewRequest(http.MethodPost, cfg.EthClientUrl, b)
			if err != nil {
				return err
			}

			req.Header.Add("Content-Type", "application/json")
			req.Header.Add("Accept", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				logger.Error(err, "could not make http request to Tenderly")

				err = errors.New("could not simulate transaction")

				txHashes.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
					return OpHashes{
						Error:  multierror.Append(oldValue.Error, err),
						Solved: currentOpHash,
					}, false
				})
				return err
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
					return err
				}

				err := errors.New(errResp.Error.Message)

				logger.Error(err, "an error occured while making simulation request",
					"status_code", resp.StatusCode,
					"slug", errResp.Error.Slug,
					"error_id", errResp.Error.ID)

				txHashes.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
					return OpHashes{
						Error:  multierror.Append(oldValue.Error, err),
						Solved: currentOpHash,
					}, false
				})

				return err
			}

			var simulatedResponse simulationResponse

			if err := json.NewDecoder(resp.Body).Decode(&simulatedResponse); err != nil {
				logger.Error(err, "could not decode simulation response")
				return err
			}

			if !simulatedResponse.Result.Status {
				// all failures have an "execution reverted" message
				logger.Error(errors.New("execution reverted"), "could not simulate transaction")

				err = errors.New("could not simulate transaction. execution reverted")

				txHashes.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
					return OpHashes{
						Error:  multierror.Append(oldValue.Error, err),
						Solved: currentOpHash,
					}, false
				})

				return err
			}

			outgoingAssetChanges := simulatedResponse.Result.AssetChanges[0]

			if common.HexToAddress(outgoingAssetChanges.From).Hex() != userop.Sender.Hex() {
				err = errors.New("first outward transaction does not belong to the userop sender")

				logger.Error(err, "unexpected asset_changes structure")

				txHashes.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
					return OpHashes{
						Error:  multierror.Append(oldValue.Error, err),
						Solved: currentOpHash,
					}, false
				})

				return err
			}

			incomingAssetChanges := simulatedResponse.Result.AssetChanges[len(simulatedResponse.Result.AssetChanges)-1]

			if common.HexToAddress(incomingAssetChanges.To).Hex() != userop.Sender.Hex() {
				err = errors.New("outward transaction does not belong to the userop sender")

				logger.Error(err, "unexpected asset_changes structure")

				txHashes.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
					return OpHashes{
						Error:  multierror.Append(oldValue.Error, err),
						Solved: currentOpHash,
					}, false
				})

				return err
			}

			return nil
		}

		if err := g.Wait(); err != nil {
			return err
		}

		return nil
	}
}
