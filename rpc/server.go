package rpc

import (
	"math/big"
	"net/http"
	"os"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/receipt"
	"github.com/blndgs/bundler/srv"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	rpcClient "github.com/stackup-wallet/stackup-bundler/pkg/client"
	"github.com/stackup-wallet/stackup-bundler/pkg/gas"
	"github.com/stackup-wallet/stackup-bundler/pkg/jsonrpc"
	"github.com/stackup-wallet/stackup-bundler/pkg/mempool"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// RpcAdapter extends Stackup's adapter to support our custom receipt implementation.
// It embeds the original RpcAdapter and adds our custom client for receipt handling.
type RpcAdapter struct {
	*rpcClient.RpcAdapter
	client *Client
}

// Client extends Stackup's client to support our custom receipt implementation.
// It embeds the original Client and adds custom receipt handling functionality.
type Client struct {
	*rpcClient.Client
	getUserOpReceipt receipt.GetUserOpReceiptFunc
}

// NewRpcAdapter creates a new instance of our extended RPC adapter.
// Parameters:
//   - client: Our extended client implementation
//   - debug: Stackup's debug client for additional functionality
//
// Returns:
//   - A new RPC adapter instance that supports our custom receipt
func NewRpcAdapter(
	client *Client,
	debug *rpcClient.Debug) *RpcAdapter {
	// Create Stackup's adapter with the base client
	baseAdapter := rpcClient.NewRpcAdapter(client.Client, debug)

	return &RpcAdapter{
		RpcAdapter: baseAdapter,
		client:     client,
	}
}

// NewClient creates a new instance of our extended client.
// Parameters:
//   - chainID: The blockchain chain ID
//   - mempool: The mempool instance for transaction management
//   - values: Configuration values
//   - ov: Gas overhead calculator
//
// Returns:
//   - A new client instance with our custom extensions
func NewClient(
	chainID *big.Int,
	mempool *mempool.Mempool,
	values *conf.Values,
	ov *gas.Overhead,
) *Client {
	client := rpcClient.New(
		mempool,
		gas.NewDefaultOverhead(),
		chainID,
		values.SupportedEntryPoints,
		values.OpLookupLimit)
	return &Client{
		Client: client,
	}
}

// SetGetUserOpReceiptFunc sets the function used to get operation receipts.
// This allows for custom receipt handling with our extended receipt type.
func (c *Client) SetGetUserOpReceiptFunc(fn receipt.GetUserOpReceiptFunc) {
	c.getUserOpReceipt = fn
}

// GetUserOperationReceipt retrieves the receipt for a user operation.
// It uses our custom receipt type that includes additional fields 'reason'.
func (c *Client) GetUserOperationReceipt(hash string) (*receipt.UserOperationReceipt, error) {
	values := conf.GetValues()
	return c.getUserOpReceipt(hash, values.SupportedEntryPoints[0], values.OpLookupLimit)
}

// Eth_getUserOperationReceipt implements the RPC method for retrieving operation receipts.
// It overrides the standard implementation to use our custom receipt type.
func (r *RpcAdapter) Eth_getUserOperationReceipt(
	userOpHash string,
) (*receipt.UserOperationReceipt, error) {
	return r.client.GetUserOperationReceipt(userOpHash)
}

// NewRPCServer creates and configures the RPC server with all necessary middleware and handlers.
// Parameters:
//   - values: Configuration values
//   - logger: Logger instance
//   - relayer: Transaction relayer
//   - rpcAdapter: Our custom RPC adapter
//   - ethClient: Ethereum client
//   - rpcClient: RPC client
//   - chainID: Blockchain chain ID
//
// Returns:
//   - Configured Gin engine
//   - Cleanup function for graceful shutdown
func NewRPCServer(
	values *conf.Values,
	logger logr.Logger,
	relayer *srv.Relayer,
	rpcAdapter *RpcAdapter,
	ethClient *ethclient.Client,
	rpcClient *rpc.Client,
	chainID *big.Int) (*gin.Engine, func()) {
	// Set Gin mode based on configuration
	gin.SetMode(values.GinMode)

	r := gin.New()

	// Configure trusted proxies
	if err := r.SetTrustedProxies(nil); err != nil {
		logger.Error(err, "could not set up trusted proxies")
		os.Exit(1)
	}

	// Initialize OpenTelemetry if enabled
	var teardown = func() {}

	if values.OTELIsEnabled {
		teardown = initOTELCapabilities(&options{
			ServiceName:  values.ServiceName,
			CollectorUrl: values.OTELCollectorEndpoint,
			InsecureMode: true,
			ChainID:      chainID,
			Address:      common.HexToAddress(values.Beneficiary),
		}, logger)
		r.Use(otelgin.Middleware(values.ServiceName))
	}

	// Configure middleware
	r.Use(
		cors.Default(),
		WithLogr(logger),
		gin.Recovery(),
	)

	// Health check endpoint
	r.GET("/health", func(g *gin.Context) {
		g.JSON(http.StatusOK, gin.H{
			"status":        http.StatusOK,
			"message":       "Server is up and running.",
			"commit_id":     conf.GetGitCommitID(),
			"model_version": conf.GetModelVersion(),
		})
	})

	// Configure RPC handlers
	handlers := []gin.HandlerFunc{
		ExtERC4337Controller(relayer.GetOpHashes(), rpcAdapter, rpcClient,
			ethClient, values, logger),
		jsonrpc.WithOTELTracerAttributes(),
	}

	// Register RPC endpoints
	r.POST("/", handlers...)
	r.POST("/rpc", handlers...)

	return r, teardown
}
