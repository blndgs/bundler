package rpc

import (
	"math/big"
	"net/http"
	"os"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/srv"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stackup-wallet/stackup-bundler/pkg/client"
	"github.com/stackup-wallet/stackup-bundler/pkg/jsonrpc"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

func NewRPCServer(values *conf.Values, logger logr.Logger, relayer *srv.Relayer,
	rpcAdapter *client.RpcAdapter, ethClient *ethclient.Client,
	rpcClient *rpc.Client, chainID *big.Int) (*gin.Engine, func()) {

	gin.SetMode(values.GinMode)

	r := gin.New()

	if err := r.SetTrustedProxies(nil); err != nil {
		logger.Error(err, "could not set up trusted proxies")
		os.Exit(1)
	}

	var teardown = func() {}

	if values.OTELIsEnabled {
		logger.Info("OTEL is enabled")

		teardown = initOTELCapabilities(&options{
			ServiceName:  values.ServiceName,
			CollectorUrl: values.OTELCollectorEndpoint,
			InsecureMode: true,
			ChainID:      chainID,
			Address:      common.HexToAddress(values.Beneficiary),
		}, logger)
		r.Use(otelgin.Middleware(values.ServiceName))

		metrics := r.Group("/metrics")
		{
			metrics.GET("", gin.WrapH(promhttp.Handler()))
		}
	} else {
		logger.Info("OTEL is not enabled")
	}

	r.Use(
		cors.Default(),
		WithLogr(logger),
		gin.Recovery(),
	)

	r.GET("/health", func(g *gin.Context) {
		g.JSON(http.StatusOK, gin.H{
			"status":        http.StatusOK,
			"message":       "Server is up and running.",
			"commit_id":     conf.GetGitCommitID(),
			"model_version": conf.GetModelVersion(),
		})
	})

	handlers := []gin.HandlerFunc{
		ExtERC4337Controller(relayer.GetOpHashes(), rpcAdapter, rpcClient,
			ethClient, values, logger),
		jsonrpc.WithOTELTracerAttributes(),
	}

	r.POST("/", handlers...)
	r.POST("/rpc", handlers...)

	return r, teardown
}
