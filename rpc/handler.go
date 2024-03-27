package main

import (
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-gonic/gin"
	"github.com/goccy/go-json"
	"github.com/stackup-wallet/stackup-bundler/pkg/jsonrpc"
)

// ExtERC4337Controller extends the default JSON-RPC controller to handle non-ERC4337 Ethereum RPC methods.
func ExtERC4337Controller(api interface{}, rpcClient *rpc.Client, ethRPCClient *ethclient.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		originalHandler := jsonrpc.Controller(api)
		originalHandler(c)

		// Check if the request has already been handled by the original Controller
		if c.Writer.Written() {
			return
		}

		// Get the JSON-RPC request data from the Gin context
		requestData, exists := c.Get("json-rpc-request")
		if !exists {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON-RPC request"})
			return
		}

		// Extract the RPC method from the request data
		method, ok := requestData.(map[string]any)["method"].(string)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Missing or invalid RPC method"})
			return
		}

		if isStdEthereumRPCMethod(method) {
			fmt.Println("Method:", method)

			if requestDataMap, ok := requestData.(map[string]any); ok {
				// Proxy the request to the Ethereum node
				routeStdEthereumRPCRequest(c, method, rpcClient, ethRPCClient, requestDataMap)
			}

			return
		}
	}
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

func routeStdEthereumRPCRequest(c *gin.Context, method string, rpcClient *rpc.Client, ethClient *ethclient.Client, requestData map[string]any) {
	const (
		ethCall = "eth_call"
	)

	switch strings.ToLower(method) {
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
