package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/stackup-wallet/stackup-bundler/pkg/altmempools"
	"github.com/stackup-wallet/stackup-bundler/pkg/bundler"
	"github.com/stackup-wallet/stackup-bundler/pkg/client"
	"github.com/stackup-wallet/stackup-bundler/pkg/gas"
	"github.com/stackup-wallet/stackup-bundler/pkg/jsonrpc"
	"github.com/stackup-wallet/stackup-bundler/pkg/mempool"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/batch"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/checks"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/entities"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/expire"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/gasprice"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/relay"
	"github.com/stackup-wallet/stackup-bundler/pkg/signer"
	"go.opentelemetry.io/otel"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/validations"
)

func main() {
	values := conf.GetValues()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up a signal handler to shutdown gracefully on SIGINT or SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	eoa, err := signer.New(values.PrivateKey)
	if err != nil {
		log.Fatal(err)
	}
	beneficiary := common.HexToAddress(values.Beneficiary)

	rpcClient, err := rpc.Dial(values.EthClientUrl)
	if err != nil {
		log.Fatalf("Failed to connect to Ethereum node: %v", err)
	}
	eth := ethclient.NewClient(rpcClient)

	db, err := badger.Open(badger.DefaultOptions(values.DataDirectory))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	runDBGarbageCollection(db)

	mem, err := mempool.New(db)
	if err != nil {
		log.Fatal(err)
	}

	chain, err := eth.ChainID(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	alt, err := altmempools.NewFromIPFS(chain, "", []string{})
	if err != nil {
		log.Fatal(err)
	}

	validator := validations.New(
		db,
		rpcClient,
		gas.NewDefaultOverhead(),
		alt,
		values.MaxVerificationGas,
		values.MaxBatchGasLimit,
		false, // isRIP7212Supported
		values.NativeBundlerCollectorTracer,
		conf.NewReputationConstantsFromEnv(),
	)

	exp := expire.New(time.Second * values.MaxOpTTL)

	rep := entities.New(db, eth, conf.NewReputationConstantsFromEnv())
	logger := NewLogger()

	relayer := relay.New(eoa, eth, chain, beneficiary, logger)

	c := client.New(mem, gas.NewDefaultOverhead(), chain, values.SupportedEntryPoints, values.OpLookupLimit)
	c.SetGetUserOpReceiptFunc(client.GetUserOpReceiptWithEthClient(eth))
	c.SetGetGasPricesFunc(client.GetGasPricesWithEthClient(eth))
	c.SetGetGasEstimateFunc(
		client.GetGasEstimateWithEthClient(
			rpcClient,
			gas.NewDefaultOverhead(),
			chain,
			values.MaxBatchGasLimit,
			values.NativeBundlerExecutorTracer,
		),
	)
	c.SetGetUserOpByHashFunc(client.GetUserOpByHashWithEthClient(eth))
	c.UseLogger(logger)

	c.UseModules(
		rep.CheckStatus(),
		rep.ValidateOpLimit(),
		validator.OpValues(),
		// Omit simulation
		rep.IncOpsSeen(),
	)

	b := bundler.New(mem, chain, values.SupportedEntryPoints)
	b.SetGetBaseFeeFunc(gasprice.GetBaseFeeWithEthClient(eth))
	b.SetGetGasTipFunc(gasprice.GetGasTipWithEthClient(eth))
	b.SetGetLegacyGasPriceFunc(gasprice.GetLegacyGasPriceWithEthClient(eth))
	b.UseLogger(logger)
	if err := b.UserMeter(otel.GetMeterProvider().Meter("bundler")); err != nil {
		log.Fatal(err)
	}

	var check = (*checks.Standalone)(unsafe.Pointer(validator))
	b.UseModules(
		exp.DropExpired(),
		batch.SortByNonce(),
		batch.MaintainGasLimit(values.MaxBatchGasLimit),
		relayer.SendUserOperation(),
		rep.IncOpsIncluded(),
		check.Clean(),
	)

	if err := b.Run(); err != nil {
		log.Fatal(err)
	}

	var d = client.NewDebug(eoa, eth, mem, rep, b, chain, values.SupportedEntryPoints[0], beneficiary)
	b.SetMaxBatch(1)
	relayer.SetWaitTimeout(0)

	// Init HTTP server
	gin.SetMode(gin.DebugMode)
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatal(err)
	}
	// TODO investigate usefulness
	// if o11y.IsEnabled(conf.OTELServiceName) {
	// 	r.Use(otelgin.Middleware(conf.OTELServiceName))
	// }
	r.Use(
		cors.Default(),
		WithLogr(logger),
		gin.Recovery(),
	)
	r.GET("/ping", func(g *gin.Context) {
		g.Status(http.StatusOK)
	})
	handlers := []gin.HandlerFunc{
		ExtERC4337Controller(client.NewRpcAdapter(c, d), rpcClient, eth),
		jsonrpc.WithOTELTracerAttributes(),
	}
	r.POST("/", handlers...)
	r.POST("/rpc", handlers...)

	if err := r.Run(fmt.Sprintf(":%d", values.Port)); err != nil {
		log.Fatal(err)
	}

	// Wait for the context to be canceled
	<-ctx.Done()
	log.Println("Shutting down...")
}

func NewLogger() zerologr.Logger {
	zl := zerolog.New(os.Stderr).With().Caller().Timestamp().Logger()
	logger := zerologr.New(&zl)
	return logger
}

func runDBGarbageCollection(db *badger.DB) {
	go func(db *badger.DB) {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
		again:
			err := db.RunValueLogGC(0.7)
			if err == nil {
				goto again
			}
		}
	}(db)
}

// WithLogr uses a logger with the go-logr/logr interface to log a gin HTTP request.
func WithLogr(logger logr.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {

		start := time.Now() // Start timer
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// Process request
		c.Next()

		// Fill the params
		param := gin.LogFormatterParams{}

		param.TimeStamp = time.Now() // Stop timer
		param.Latency = param.TimeStamp.Sub(start)
		if param.Latency > time.Minute {
			param.Latency = param.Latency.Truncate(time.Second)
		}

		param.ClientIP = GetClientIPFromXFF(c)
		param.Method = c.Request.Method
		param.StatusCode = c.Writer.Status()
		param.ErrorMessage = c.Errors.ByType(gin.ErrorTypePrivate).String()
		param.BodySize = c.Writer.Size()
		if raw != "" {
			path = path + "?" + raw
		}
		param.Path = path

		logEvent := logger.WithName("http").
			WithValues("client_id", param.ClientIP).
			WithValues("method", param.Method).
			WithValues("status_code", param.StatusCode).
			WithValues("body_size", param.BodySize).
			WithValues("path", param.Path).
			WithValues("latency", param.Latency.String())

		req, exists := c.Get("json-rpc-request")
		if exists {
			json := req.(map[string]any)
			logEvent = logEvent.WithValues("rpc_method", json["method"])
		}

		// Log using the params
		if c.Writer.Status() >= 500 {
			logEvent.Error(errors.New(param.ErrorMessage), param.ErrorMessage)
		} else {
			logEvent.Info(param.ErrorMessage)
		}
	}
}
