package solution

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"unsafe"

	"github.com/blndgs/model"
	"github.com/goccy/go-json"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
)

// ValidateIntents returns a BatchHandlerFunc that will
// send the batch of UserOperations to the Solver
// in other to validate if the userops are valid or not
func (ei *IntentsHandler) ValidateIntents() modules.BatchHandlerFunc {
	return func(ctx *modules.BatchHandlerCtx) error {

		batchIntentIndices := make(batchIntentIndices)

		modelUserOps := *(*[]*model.UserOperation)(unsafe.Pointer(&ctx.Batch))

		body := ei.bufferIntentOps(ctx.EntryPoint, ctx.ChainID, batchIntentIndices, modelUserOps)

		if len(body.UserOps) == 0 {
			return nil
		}

		return ei.sendToSolverForValidation(body)
	}
}

// sendToSolverForValidation  sends the batch of UserOperations to the Solver.
// to validate them
func (ei *IntentsHandler) sendToSolverForValidation(body model.BodyOfUserOps) error {

	parsedURL, err := url.Parse(ei.SolverURL)
	if err != nil {
		return err
	}

	parsedURL.Path = "/validate"
	parsedURL.RawQuery = ""
	parsedURL.Fragment = ""

	solverURL := parsedURL.String()

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, solverURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := ei.SolverClient.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("solver returned non-OK status: %s", resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(&body)
}
