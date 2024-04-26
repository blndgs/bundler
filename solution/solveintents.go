// Package solution sends the received bundler batch of Intent UserOperations
// to the Solver to solve the Intent and fill-in the EVM instructions.
//
// This implementation makes 1 attempt for each Intent userOp to be solved.
//
// Solved userOps update the received bundle
// All other returned statuses result in dropping those userOps
// from the batch.
// Received are treated as expired because they may have been compressed to
// Solved Intents.
//
// The Solver may return a subset and in different sequence the UserOperations
// and a matching occurs by the hash value of each UserOperation to the bundle
// UserOperation.
package solution

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"time"
	"unsafe"

	"github.com/blndgs/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/goccy/go-json"
	"github.com/pkg/errors"

	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
)

// userOpHashID is the hash value of the UserOperation
type opHashID string

// batchOpIndex is the index of the UserOperation in the Bundler batch
type batchOpIndex int

// batchIntentIndices buffers the mapping of the UserOperation hash value -> the index of the UserOperation in the batch
type batchIntentIndices map[opHashID]batchOpIndex

type IntentsHandler struct {
	SolverURL    string
	SolverClient *http.Client
}

func New(solverURL string) *IntentsHandler {
	const httpClientTimeout = 100 * time.Second

	return &IntentsHandler{
		SolverURL:    solverURL,
		SolverClient: &http.Client{Timeout: httpClientTimeout},
	}
}

// bufferIntentOps caches the index of the userOp in the received batch and creates the UserOperationExt slice for the
// Solver with cached Hashes and ProcessingStatus set to `Received`.
func (ei *IntentsHandler) bufferIntentOps(entrypoint common.Address, chainID *big.Int, batchIndices batchIntentIndices, userOpBatch []*model.UserOperation) model.BodyOfUserOps {
	body := model.BodyOfUserOps{
		UserOps:    make([]*model.UserOperation, 0, len(userOpBatch)),
		UserOpsExt: make([]model.UserOperationExt, 0, len(userOpBatch)),
	}
	for idx, op := range userOpBatch {
		if op.HasIntent() {
			hashID := op.GetUserOpHash(entrypoint, chainID).String()

			// Don't mutate the original op
			clonedOp := *op
			body.UserOps = append(body.UserOps, &clonedOp)

			body.UserOpsExt = append(body.UserOpsExt, model.UserOperationExt{
				OriginalHashValue: hashID,
				// Cache hash before it changes
				ProcessingStatus: model.Received,
			})

			// Reverse caching
			batchIndices[opHashID(hashID)] = batchOpIndex(idx)
		}
	}

	return body
}

// SolveIntents returns a BatchHandlerFunc that will send the batch of UserOperations to the Solver
// and those solved to be sent on chain.
func (ei *IntentsHandler) SolveIntents() modules.BatchHandlerFunc {
	return func(ctx *modules.BatchHandlerCtx) error {
		batchIntentIndices := make(batchIntentIndices)

		// Verify structural congruence for guaranteeing the following type assertion
		var _ = model.UserOperation(userop.UserOperation{})

		// cast the received userOp batch to a slice of model.UserOperation
		// to be sent to the Solver
		modelUserOps := *(*[]*model.UserOperation)(unsafe.Pointer(&ctx.Batch))

		println("Received batch of UserOperations for solution: ", len(modelUserOps))
		for idx, op := range modelUserOps {
			println("Received UserOperation: [", idx, "], isIntent", op.HasIntent(), "op:", op.String())
		}

		// Prepare the body to send to the Solver
		body := ei.bufferIntentOps(ctx.EntryPoint, ctx.ChainID, batchIntentIndices, modelUserOps)

		// Intents to process
		if len(body.UserOps) == 0 {

			return nil
		}

		if err := ei.sendToSolver(body); err != nil {
			return err
		}

		for idx, opExt := range body.UserOpsExt {
			batchIndex := batchIntentIndices[opHashID(body.UserOpsExt[idx].OriginalHashValue)]
			// print to stdout the userOp and Intent JSON
			fmt.Println("Solver response, status:", opExt.ProcessingStatus, ", batchIndex:", batchIndex, ", hash:", body.UserOpsExt[idx].OriginalHashValue)
			switch opExt.ProcessingStatus {
			case model.Unsolved, model.Expired, model.Invalid, model.Received:
				// dropping further processing
				ctx.MarkOpIndexForRemoval(int(batchIndex), string("intent uo not solved:"+opExt.ProcessingStatus))
				println()
				println("****************************************************")
				println("Solver dropping userOp: ", body.UserOps[idx].String(), " with status: ", opExt.ProcessingStatus)
				println("****************************************************")
				println()
			case model.Solved:
				// set the solved userOp values to the received batch's userOp values
				ctx.Batch[batchIndex].CallData = make([]byte, len(body.UserOps[idx].CallData))
				copy(ctx.Batch[batchIndex].CallData, body.UserOps[idx].CallData)
				ctx.Batch[batchIndex].Signature = make([]byte, len(body.UserOps[idx].Signature))
				copy(ctx.Batch[batchIndex].Signature, body.UserOps[idx].Signature)
				ctx.Batch[batchIndex].CallGasLimit = body.UserOps[idx].CallGasLimit
				ctx.Batch[batchIndex].VerificationGasLimit = body.UserOps[idx].VerificationGasLimit
				ctx.Batch[batchIndex].PreVerificationGas = body.UserOps[idx].PreVerificationGas
				ctx.Batch[batchIndex].MaxFeePerGas = body.UserOps[idx].MaxFeePerGas
				ctx.Batch[batchIndex].MaxPriorityFeePerGas = body.UserOps[idx].MaxPriorityFeePerGas

			default:
				return errors.Errorf("unknown processing status: %s", opExt.ProcessingStatus)
			}
		}

		return nil
	}
}

func ReportSolverHealth(solverURL string) error {
	parsedURL, err := url.Parse(solverURL)
	if err != nil {
		println("Error parsing Solver URL: ", solverURL, ", ", err)
		return err
	}

	parsedURL.Path = "/health"
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""

	solverURL = parsedURL.String()
	fmt.Println("Requesting solver health at ", solverURL)

	handler := New(solverURL)

	req, err := http.NewRequest(http.MethodGet, solverURL, nil)
	if err != nil {
		return err
	}

	resp, err := handler.SolverClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("solver health check returned non-OK status: %s", resp.Status)
	}

	fmt.Println("Solver health response: ", resp.Status)
	_, err = io.Copy(os.Stdout, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

// sendToSolver sends the batch of UserOperations to the Solver.
func (ei *IntentsHandler) sendToSolver(body model.BodyOfUserOps) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, ei.SolverURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := ei.SolverClient.Do(req)
	if err != nil {
		println("Solver request failed at URL: ", ei.SolverURL)
		println("Solver error: ", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("solver returned non-OK status: %s", resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(&body)
}
