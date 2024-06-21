package solution

import (
	"bytes"
	"fmt"
	"net/http"
	"unsafe"

	"github.com/blndgs/model"
	"github.com/goccy/go-json"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
)

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

func (ei *IntentsHandler) sendToSolverForValidation(body model.BodyOfUserOps) error {
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
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("solver returned non-OK status: %s", resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(&body)
}
