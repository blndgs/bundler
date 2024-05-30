package rpc

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/blndgs/model"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
	"github.com/mitchellh/mapstructure"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/client"
	"github.com/stackup-wallet/stackup-bundler/pkg/jsonrpc"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/srv"
)

// ExtERC4337Controller extends the default JSON-RPC controller to handle non-ERC4337 Ethereum RPC methods.
func ExtERC4337Controller(hashesMap *xsync.MapOf[string, srv.OpHashes], rpcAdapter *client.RpcAdapter,
	rpcClient *rpc.Client, ethRPCClient *ethclient.Client, values *conf.Values) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != "POST" {
			jsonrpcError(c, -32700, "Parse error", "POST method excepted", nil)
			return
		}

		if c.Request.Body == nil {
			jsonrpcError(c, -32700, "Parse error", "No POST data", nil)
			return
		}

		body, err := readBody(c)
		if err != nil {
			jsonrpcError(c, -32700, "Parse error", "Error while reading request body", nil)
			return
		}

		data := make(map[string]any)
		err = json.Unmarshal(body, &data)
		if err != nil {
			jsonrpcError(c, -32700, "Parse error", "Error parsing json request", nil)
			return
		}

		id, ok := parseRequestId(data)
		if !ok {
			jsonrpcError(c, -32600, "Invalid Request", "No or invalid 'id' in request", nil)
			return
		}

		if data["jsonrpc"] != "2.0" {
			jsonrpcError(c, -32600, "Invalid Request", "Version of jsonrpc is not 2.0", &id)
			return
		}

		method, ok := data["method"].(string)
		if !ok {
			jsonrpcError(c, -32600, "Invalid Request", "No or invalid 'method' in request", &id)
			return
		}

		println()
		println("-------------------------------")
		println("Method:", method)
		println("-------------------------------")
		println()

		if isStdEthereumRPCMethod(method) || strings.ToLower(method) == "eth_senduseroperation" {
			routeStdEthereumRPCRequest(c, rpcAdapter, method, rpcClient, ethRPCClient, hashesMap, data, values)

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

	// Check if the method is NOT a bundler-specific method
	_, isBundlerMethod := bundlerMethods[strings.ToLower(method)]

	return !isBundlerMethod
}

func routeStdEthereumRPCRequest(c *gin.Context, rpcAdapter *client.RpcAdapter, method string, rpcClient *rpc.Client, ethClient *ethclient.Client,
	hashesMap *xsync.MapOf[string, srv.OpHashes], requestData map[string]any, values *conf.Values) {

	const (
		ethCall = "eth_call"
	)

	switch strings.ToLower(method) {
	case "eth_senduseroperation":
		handleEthSendUserOperation(c, rpcAdapter, ethClient, hashesMap, requestData, values)
	case ethCall:
		handleEthCallRequest(c, ethClient, requestData)
	default:
		handleEthRequest(c, method, rpcClient, requestData)
	}
}

func handleEthRequest(c *gin.Context, method string, rpcClient *rpc.Client, requestData map[string]any) {
	// Extract params and keep them in their original type
	params, ok := requestData["params"].([]interface{})
	if !ok {
		jsonrpcError(c, -32602, "Invalid params format", "Expected a slice of parameters", nil)
		return
	}

	// Call the method with the parameters
	raw, err := rpcCall(c, method, rpcClient, params)
	if err != nil {
		return
	}

	sendRawJson(c, raw, requestData["id"])
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

func rpcCall(c *gin.Context, method string, rpcClient *rpc.Client, params []interface{}) (json.RawMessage, error) {
	var raw json.RawMessage
	err := rpcClient.CallContext(c, &raw, method, params...)
	if err != nil {
		jsonrpcError(c, -32603, "Internal error", err.Error(), nil)
		return nil, err
	}
	return raw, nil
}

func sendRawJson(c *gin.Context, raw json.RawMessage, id any) {
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)

	// Construct the JSON response manually
	response := fmt.Sprintf(`{"result": %s, "jsonrpc": "2.0", "id": %v}`, raw, id)

	// Write the response
	_, writeErr := c.Writer.Write([]byte(response))
	if writeErr != nil {
		// Handle error in writing response
		jsonrpcError(c, -32603, "Internal error", writeErr.Error(), nil)
	}
}

func handleEthCallRequest(c *gin.Context, ethClient *ethclient.Client, requestData map[string]any) {
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

	var blockNumber *big.Int
	if len(params) > 1 {
		blockParam := params[1].(string)
		if blockParam != "latest" {
			var intBlockNumber int64
			intBlockNumber, err := strconv.ParseInt(blockParam, 10, 64)
			if err != nil {
				jsonrpcError(c, -32602, "Invalid params", "Third parameter should be a block number or 'latest'", nil)
				return
			}
			blockNumber = big.NewInt(intBlockNumber)
		}
	}

	result, err := ethClient.CallContract(c, callMsg, blockNumber)
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

func waitForUserOpCompletion(ctx context.Context, ethClient *ethclient.Client, txHashes *xsync.MapOf[string, srv.OpHashes],
	userOpHash common.Hash, waitTimeout time.Duration) (*HashesResponse, error) {

	const (
		ticking = 500 * time.Millisecond
	)

	ticker := time.NewTicker(ticking)
	defer ticker.Stop()

	timeoutCtx, cancelTimeoutCtx := context.WithTimeout(context.Background(), waitTimeout)
	defer cancelTimeoutCtx()

	for {
		select {
		case <-ticker.C:

			opHash := userOpHash.String()

			if txHashes, ok := txHashes.Load(opHash); ok { // Retrieve the transaction hash from sync.Map

				receipt, err := ethClient.TransactionReceipt(ctx, txHashes.Trx)
				if err != nil {
					if errors.Is(err, ethereum.NotFound) {
						// Transaction not mined yet, continue waiting
						continue
					}

					return nil, fmt.Errorf("hash not found or has been dropped (%s)", opHash)
				}

				return &HashesResponse{
					Success:      receipt.Status == types.ReceiptStatusSuccessful,
					OriginalHash: opHash,
					SolvedHash:   txHashes.Solved,
					Trx:          txHashes.Trx.String(),
				}, err
			}

		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("timeout waiting for user operation completion")

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func handleEthSendUserOperation(c *gin.Context, rpcAdapter *client.RpcAdapter, ethClient *ethclient.Client,
	hashesMap *xsync.MapOf[string, srv.OpHashes], requestData map[string]any, values *conf.Values) {

	var op map[string]any
	if err := mapstructure.Decode(requestData["params"].([]interface{})[0], &op); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user operation"})
		return
	}

	ep := requestData["params"].([]interface{})[1].(string)

	uo, err := userop.New(op)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse user operation: %s", err)})
		return
	}

	mUo := (model.UserOperation)(*uo)
	if mUo.HasIntent() {
		i, err := mUo.GetIntent()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse intent: %s", err)})
			return
		}

		uoSender := mUo.Sender.String()
		if strings.ToLower(uoSender) != strings.ToLower(i.Sender) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(
				"sender address in user operation %s does not match the intent %s", uoSender, i.Sender)})
			return
		}
	}

	userOpHash, err := rpcAdapter.Eth_sendUserOperation(op, ep)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp, err := waitForUserOpCompletion(c.Request.Context(), ethClient, hashesMap, common.HexToHash(userOpHash), values.StatusTimeout)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	jsonResp, err := json.Marshal(resp)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	sendRawJson(c, json.RawMessage(jsonResp), requestData["id"])
}
