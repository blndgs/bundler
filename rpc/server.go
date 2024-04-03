package rpc

import (
	"log"
	"math/big"
	"net/http"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/srv"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/stackup-wallet/stackup-bundler/pkg/client"
	"github.com/stackup-wallet/stackup-bundler/pkg/jsonrpc"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

func NewServer(values *conf.Values, logger logr.Logger, relayer *srv.Relayer,
	rpcAdapter *client.RpcAdapter, ethClient *ethclient.Client,
	rpcClient *rpc.Client, chainID *big.Int) (*gin.Engine, func()) {

	gin.SetMode(values.GinMode)

	r := gin.New()

	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatal(err)
	}

	var teardown = func() {}

	if values.OTELIsEnabled {
		teardown = initTracer(&options{
			ServiceName:  values.OTELServiceName,
			CollectorUrl: values.OTELCollectorUrl,
			InsecureMode: true,
			ChainID:      chainID,
			Address:      common.HexToAddress(values.Beneficiary),
		})
		r.Use(otelgin.Middleware(values.OTELServiceName))
	}

	r.Use(
		cors.Default(),
		WithLogr(logger),
		gin.Recovery(),
	)

	r.GET("/ping", func(g *gin.Context) {
		g.Status(http.StatusOK)
	})

	handlers := []gin.HandlerFunc{
		ExtERC4337Controller(relayer.GetOpHashes(), rpcAdapter, rpcClient, ethClient, values),
		jsonrpc.WithOTELTracerAttributes(),
	}

	r.POST("/", handlers...)
	r.POST("/rpc", handlers...)

	return r, teardown
}
