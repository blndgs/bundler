package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/blndgs/model"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/goccy/go-json"
	"github.com/mitchellh/mapstructure"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/client"
	"github.com/stackup-wallet/stackup-bundler/pkg/jsonrpc"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/internal/metrics"
	"github.com/blndgs/bundler/srv"
	"github.com/blndgs/bundler/utils"
)

var bundlerMethods = map[string]bool{
	"eth_senduseroperation":         true,
	"eth_estimateuseroperationgas":  true,
	"eth_getuseroperationreceipt":   true,
	"eth_getuseroperationbyhash":    true,
	"eth_supportedentrypoints":      true,
	"eth_chainid":                   true,
	"debug_bundler_clearstate":      true,
	"debug_bundler_dumpmempool":     true,
	"debug_bundler_sendbundlenow":   true,
	"debug_bundler_setbundlingmode": true,
	// Add any other bundler-specific methods here
}

const (
	ethSendOpMethod = "eth_senduseroperation"
	ethCall         = "eth_call"

	statusCheckTickingInterval = 500 * time.Millisecond
)

// ExtERC4337Controller extends the default JSON-RPC controller to handle non-ERC4337 Ethereum RPC methods.
func ExtERC4337Controller(hashesMap *xsync.MapOf[string, srv.OpHashes],
	rpcAdapter *client.RpcAdapter, rpcClient *rpc.Client,
	ethRPCClient *ethclient.Client, values *conf.Values,
	logger logr.Logger, bundlerMetrics *metrics.BundlerMetrics) gin.HandlerFunc {

	return func(c *gin.Context) {
		logger = logger.WithValues("method", "ExtERC4337Controller")

		ctx, span := utils.GetTracer().Start(c.Request.Context(), "ExtERC4337Controller")
		defer span.End()

		if c.Request.Method != http.MethodPost {
			jsonrpcError(c, -32700, "Parse error", "POST method excepted", nil)
			err := errors.New("only POST method accepted")
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			return
		}

		if c.Request.Body == nil {
			jsonrpcError(c, -32700, "Parse error", "No POST data", nil)
			err := errors.New("POST data must be present")
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			return
		}

		body, err := readBody(c)
		if err != nil {
			logger.Error(err, "could not read HTTP request body")
			jsonrpcError(c, -32700, "Parse error", "Error while reading request body", nil)
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			return
		}

		data := make(map[string]any)
		err = json.Unmarshal(body, &data)
		if err != nil {
			logger.Error(err, "could not unmarshal json request body")
			jsonrpcError(c, -32700, "Parse error", "Error parsing json request", nil)
			span.SetStatus(codes.Error, err.Error())
			span.RecordError(err)
			return
		}

		id, ok := parseRequestId(data)
		if !ok {
			logger.Error(errors.New("could not parse request id"), "Invalid request")
			jsonrpcError(c, -32600, "Invalid Request", "No or invalid 'id' in request", nil)
			span.SetStatus(codes.Error, "id must be present in request")
			span.RecordError(errors.New("id must be present in request"))
			return
		}

		if data["jsonrpc"] != "2.0" {
			jsonrpcError(c, -32600, "Invalid Request", "Version of jsonrpc is not 2.0", &id)
			span.SetStatus(codes.Error, "jsonrpc version must be 2.0")
			span.RecordError(errors.New("jsonrpc version must be 2.0"))
			return
		}

		method, ok := data["method"].(string)
		if !ok {
			jsonrpcError(c, -32600, "Invalid Request", "No or invalid 'method' in request", &id)
			span.SetStatus(codes.Error, "no rpc method in request")
			span.RecordError(errors.New("no rpc method in request"))
			return
		}

		logger.Info("Processing rpc request", "method", method)

		if isStdEthereumRPCMethod(method) || strings.ToLower(method) == ethSendOpMethod {
			routeStdEthereumRPCRequest(ctx, c, rpcAdapter, method, rpcClient,
				ethRPCClient, hashesMap, data, values, logger, bundlerMetrics)
			return
		}

		// Check if the request has already been handled
		if c.Writer.Written() {
			return
		}

		originalHandler := jsonrpc.Controller(rpcAdapter)
		originalHandler(c)
	}
}

// parseRequestId checks if the JSON-RPC request contains an id field that is either NULL, Number, or String.
func parseRequestId(data map[string]any) (any, bool) {
	id, ok := data["id"]
	_, isFloat64 := id.(float64)
	_, isStr := id.(string)

	if ok && (id == nil || isFloat64 || isStr) {
		return id, true
	}
	return nil, false
}

func isStdEthereumRPCMethod(method string) bool {
	// Check if the method is NOT a bundler-specific method
	_, isBundlerMethod := bundlerMethods[strings.ToLower(method)]
	return !isBundlerMethod
}

func routeStdEthereumRPCRequest(ctx context.Context,
	c *gin.Context, rpcAdapter *client.RpcAdapter,
	method string, rpcClient *rpc.Client, ethClient *ethclient.Client,
	hashesMap *xsync.MapOf[string, srv.OpHashes], requestData map[string]any,
	values *conf.Values, logger logr.Logger,
	bundlerMetrics *metrics.BundlerMetrics) {

	startTime := time.Now()

	ctx, span := utils.GetTracer().Start(c.Request.Context(), "routeStdEthereumRPCRequest")
	defer span.End()

	method = strings.ToLower(method)
	span.SetAttributes(attribute.String("rpc_method", strings.ToLower(method)))

	switch method {
	case ethSendOpMethod:

		bundlerMetrics.AddUserOpInFlight()

		handleEthSendUserOperation(ctx, c, rpcAdapter,
			ethClient, hashesMap, requestData, values,
			logger, bundlerMetrics)

		bundlerMetrics.RemoveUserOpInFlight()

	case ethCall:
		handleEthCallRequest(ctx, c, ethClient, requestData, logger)
	default:
		handleEthRequest(ctx, c, method, rpcClient, requestData, logger)
	}

	duration := time.Now().Sub(startTime)

	span.SetAttributes(
		attribute.String("duration", duration.String()))

	bundlerMetrics.TrackETHMethodCall(context.Background(), method, duration)
}

func handleEthRequest(ctx context.Context, c *gin.Context, method string, rpcClient *rpc.Client,
	requestData map[string]any, logger logr.Logger) {

	ctx, span := utils.GetTracer().Start(c.Request.Context(), "handleEthRequest")
	defer span.End()

	// Extract params and keep them in their original type
	params, ok := requestData["params"].([]interface{})
	if !ok {
		logger.Error(errors.New("params not found"), "Invalid params format")
		jsonrpcError(c, -32602, "Invalid params format", "Expected a slice of parameters", nil)
		return
	}

	// Call the method with the parameters
	raw, err := rpcCall(ctx, c, method, rpcClient, params)
	if err != nil {
		logger.Error(err, "rpc call failure")
		return
	}

	sendRawJson(ctx, c, raw, requestData["id"], logger)
}

func jsonrpcError(c *gin.Context, code int, message string, data any, id any) {
	c.JSON(http.StatusOK, gin.H{
		"jsonrpc": "2.0",
		"error": gin.H{
			"code":    code,
			"message": message,
			"data":    data,
		},
		"id": id,
	})
	c.Abort()
}

func rpcCall(ctx context.Context, c *gin.Context, method string, rpcClient *rpc.Client,
	params []interface{}) (json.RawMessage, error) {

	var raw json.RawMessage
	err := rpcClient.CallContext(ctx, &raw, method, params...)
	if err != nil {
		jsonrpcError(c, -32603, "Internal error", err.Error(), nil)
		return nil, err
	}

	return raw, nil
}

func sendRawJson(ctx context.Context, c *gin.Context, raw json.RawMessage, id any,
	logger logr.Logger) {

	ctx, span := utils.GetTracer().Start(c.Request.Context(), "sendRawJson")
	defer span.End()

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)

	// Construct the JSON response manually
	response := fmt.Sprintf(`{"result": %s, "jsonrpc": "2.0", "id": %v}`, raw, id)

	// Write the response
	_, writeErr := c.Writer.Write([]byte(response))
	if writeErr != nil {
		logger.Error(writeErr, "could not write response")
		// Handle error in writing response

		span.RecordError(writeErr)
		span.SetStatus(codes.Error, writeErr.Error())
		jsonrpcError(c, -32603, "Internal error", writeErr.Error(), nil)
	}
}

func handleEthCallRequest(ctx context.Context, c *gin.Context, ethClient *ethclient.Client,
	requestData map[string]any, logger logr.Logger) {

	ctx, span := utils.GetTracer().Start(c.Request.Context(), "handleEthCallRequest")
	defer span.End()

	params := requestData["params"].([]interface{})

	var (
		callParams map[string]interface{}
		to         string
		data       string
		callMsg    ethereum.CallMsg
	)
	if len(params) > 0 {
		// Assuming the first param is the address and the second is the data
		// This needs to be adjusted according to the specific RPC method and parameters
		ok := false
		callParams, ok = params[0].(map[string]interface{})
		if !ok {
			jsonrpcError(c, -32602, "Invalid params", "First parameter should be a map", nil)
			return
		}

		to, ok = callParams["to"].(string)
		if !ok {
			jsonrpcError(c, -32602, "Invalid params", "Contract address (to) not provided or invalid", nil)
			return
		}

		data, ok = callParams["data"].(string)
		if !ok {
			jsonrpcError(c, -32602, "Invalid params", "Data not provided or invalid", nil)
			return
		}

		address := common.HexToAddress(to)
		callMsg = ethereum.CallMsg{
			To:   &address,
			Data: common.FromHex(data),
		}
	}

	blockNumber, err := ParseAndSetBlockNumber(span, params)
	if err != nil {
		logger.Error(err, "Invalid block number")
		jsonrpcError(c, -32602, "Invalid params", err.Error(), requestData["id"])
		return
	}

	if to != "" {
		span.SetAttributes(attribute.String("to", to))
	}

	result, err := ethClient.CallContract(ctx, callMsg, blockNumber)
	// The erc-4337 spec has a special case for revert errors, where the revert data is returned as the result
	const revertErrorKey = "execution reverted"
	if err != nil && err.Error() == revertErrorKey {
		strResult := extractDataFromUnexportedError(err)
		if strResult != "" {
			c.JSON(http.StatusOK, gin.H{
				"result":  strResult,
				"jsonrpc": "2.0",
				"id":      requestData["id"],
			})

			return
		}
	}

	if err != nil {
		logger.Error(err, "rpc call to contract failed")
		jsonrpcError(c, -32603, "Internal error", err.Error(), nil)
		return
	}

	resultStr := "0x" + common.Bytes2Hex(result)

	c.JSON(http.StatusOK, gin.H{
		"result":  resultStr,
		"jsonrpc": "2.0",
		"id":      requestData["id"],
	})
}

// extractDataFromUnexportedError extracts the "Data" field from *rpc.jsonError that is not exported
// using reflection.
func extractDataFromUnexportedError(err error) string {
	if err == nil {
		return ""
	}

	val := reflect.ValueOf(err)
	if val.Kind() == reflect.Ptr && !val.IsNil() {
		// Assuming jsonError is a struct
		errVal := val.Elem()

		// Check if the struct has a field named "Data".
		dataField := errVal.FieldByName("Data")
		if dataField.IsValid() && dataField.CanInterface() {
			// Assuming the data field is a string
			return dataField.Interface().(string)
		}
	}

	return ""
}

// Make hashResponse struct public and JSON serializable

type HashesResponse struct {
	Success bool `json:"success"`
	// SDK or original unsolved user operation hash
	OriginalHash string `json:"original_hash"`
	// If different, it is the hash corresponding to the solved user operation
	SolvedHash string `json:"solved_hash"`
	// Transaction hash
	Trx string `json:"trx"`
}

func waitForUserOpCompletion(ctx context.Context, ethClient *ethclient.Client,
	txHashes *xsync.MapOf[string, srv.OpHashes],
	userOpHash common.Hash, waitTimeout time.Duration) (*HashesResponse, error) {

	ctx, span := utils.GetTracer().Start(ctx, "waitForUserOpCompletion")
	defer span.End()

	opHash := userOpHash.String()

	span.SetAttributes(
		attribute.String("userop_hash", opHash),
		attribute.Int64("wait_timeout", int64(waitTimeout)),
	)

	ticker := time.NewTicker(statusCheckTickingInterval)
	defer ticker.Stop()

	timeoutCtx, cancelTimeoutCtx := context.WithTimeout(context.Background(), waitTimeout)
	defer cancelTimeoutCtx()

	for {
		select {
		case <-ticker.C:

			// Retrieve the transaction hash from sync.Map
			opHashes, ok := txHashes.Load(opHash)
			if !ok {
				continue
			}

			span.SetAttributes(attribute.String("tx_hash", opHashes.Trx.String()))

			if opHashes.Error != nil {
				return nil, opHashes.Error
			}

			receipt, err := ethClient.TransactionReceipt(ctx, opHashes.Trx)
			if err != nil {
				if errors.Is(err, ethereum.NotFound) {
					// Transaction not mined yet, continue waiting
					continue
				}

				return nil, fmt.Errorf("hash not found or has been dropped (%s)", opHash)
			}

			span.SetAttributes(attribute.String("status", opHashes.Solved))

			return &HashesResponse{
				Success:      receipt.Status == types.ReceiptStatusSuccessful,
				OriginalHash: opHash,
				SolvedHash:   opHashes.Solved,
				Trx:          opHashes.Trx.String(),
			}, err

		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("timeout waiting for user operation completion")

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func handleEthSendUserOperation(ctx context.Context,
	c *gin.Context, rpcAdapter *client.RpcAdapter, ethClient *ethclient.Client,
	hashesMap *xsync.MapOf[string, srv.OpHashes], requestData map[string]any,
	values *conf.Values, logger logr.Logger, bundlerMetrics *metrics.BundlerMetrics) {

	ctx, span := utils.GetTracer().Start(c.Request.Context(), "handleEthSendUserOperation")
	defer span.End()

	var op map[string]any
	if err := mapstructure.Decode(requestData["params"].([]interface{})[0], &op); err != nil {
		logger.Error(err, "could not decode request params")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user operation"})
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}

	ep := requestData["params"].([]interface{})[1].(string)

	uo, err := userop.New(op)
	if err != nil {
		logger.Error(err, "could not parse userops data structure")
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse user operation: %s", err)})
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}

	mUo := (model.UserOperation)(*uo)
	if mUo.HasIntent() {
		_, err := mUo.GetIntent()
		if err != nil {
			logger.Error(err, "failed to parse intent")
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse intent: %s", err)})
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return
		}
	}

	userOpHash, err := rpcAdapter.Eth_sendUserOperation(op, ep)
	if err != nil {
		logger.Error(err, "error while sending userops on onchain")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}

	resp, err := waitForUserOpCompletion(ctx, ethClient, hashesMap, common.HexToHash(userOpHash),
		values.StatusTimeout)
	if err != nil {
		logger.Error(err, "error while fetching the status of the userops onchain transaction", "userop_hash", userOpHash)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}

	jsonResp, err := json.Marshal(resp)
	if err != nil {
		logger.Error(err, "error while parsing response from userops response", "userop_hash", userOpHash)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}

	sendRawJson(ctx, c, json.RawMessage(jsonResp), requestData["id"], logger)
}
