package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/go-logr/logr"
	"github.com/stackup-wallet/stackup-bundler/pkg/altmempools"
	"github.com/stackup-wallet/stackup-bundler/pkg/bundler"
	"github.com/stackup-wallet/stackup-bundler/pkg/client"
	"github.com/stackup-wallet/stackup-bundler/pkg/gas"
	"github.com/stackup-wallet/stackup-bundler/pkg/mempool"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/batch"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/entities"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/expire"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/gasprice"
	"github.com/stackup-wallet/stackup-bundler/pkg/signer"
	"go.opentelemetry.io/otel"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/logger"
	rpcHandler "github.com/blndgs/bundler/rpc"
	"github.com/blndgs/bundler/solution"
	"github.com/blndgs/bundler/srv"
	"github.com/blndgs/bundler/validations"
)

// build time LDFlags
var (
	CommitID, ModelVersion string
)

func main() {
	conf.SetLDFlags(CommitID, ModelVersion)
	values := conf.GetValues()

	stdLogger := logger.NewZeroLogr(values.DebugMode)

	if strings.TrimSpace(values.ServiceName) == "" {
		err := errors.New("please provide a valid service name")
		stdLogger.Error(err, "no service name provided")
		os.Exit(1)
	}

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
		stdLogger.Error(err, "could not set up signer")
		os.Exit(1)
	}

	beneficiary := common.HexToAddress(values.Beneficiary)

	rpcClient, err := rpc.Dial(values.EthClientUrl)
	if err != nil {
		stdLogger.Error(err, "Failed to connect to Ethereum node")
		os.Exit(1)
	}

	eth := ethclient.NewClient(rpcClient)

	db, err := badger.Open(badger.DefaultOptions(values.DataDirectory))
	if err != nil {
		stdLogger.Error(err, "could not open badger database")
		os.Exit(1)
	}

	defer db.Close()
	runDBGarbageCollection(db)

	mem, err := mempool.New(db)
	if err != nil {
		stdLogger.Error(err, "could not set up Mempool")
		os.Exit(1)
	}

	chain, err := eth.ChainID(context.Background())
	if err != nil {
		stdLogger.Error(err, "could not fetch chain id from RPC")
		os.Exit(1)
	}

	alt, err := altmempools.NewFromIPFS(chain, "", []string{})
	if err != nil {
		stdLogger.Error(err, "could not set up the alternative IPFS mempool")
		os.Exit(1)
	}

	h, err := os.Hostname()
	if err != nil {
		stdLogger.Error(err, "could not fetch host name of machine")
		os.Exit(1)
	}

	stdLogger = stdLogger.WithValues(
		"service", values.ServiceName,
		"host", h)

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
		stdLogger,
	)

	exp := expire.New(time.Second * values.MaxOpTTL)

	rep := entities.New(db, eth, conf.NewReputationConstantsFromEnv())

	relayer := srv.New(values.SupportedEntryPoints[0], eoa, eth, chain, beneficiary, stdLogger)

	stdLogger.Info("Using solver url", "url", values.SolverURL)

	solver := solution.New(values.SolverURL, stdLogger, relayer.GetOpHashes(), values.SupportedEntryPoints[0], chain)
	if err := solver.ReportSolverHealth(values.SolverURL); err != nil {
		stdLogger.Error(err, "could not verify Solver's healthcheck")
		os.Exit(1)
	}

	erc4337Client := createERC4337Client(mem, values, chain, eth, rpcClient, stdLogger, rep, validator)

	bundlerClient := createBundlerClient(mem, chain, values, eth, stdLogger)

	check := validator.ToStandaloneCheck()

	whitelistHandler, whitelistCleanupFn := srv.CheckSenderWhitelist(db, values.WhiteListedAddresses,
		stdLogger, relayer.GetOpHashes(),
		values.SupportedEntryPoints[0], chain)

	if whitelistHandler == nil {
		err := "could not set up sender whitelist middleware"
		stdLogger.Info(err, "whitelisting handler could not be setup")
		os.Exit(1)
	}

	simulatorHandler := srv.SimulateTxWithTenderly(eoa, values,
		eth,
		stdLogger,
		relayer.GetOpHashes(),
		values.SupportedEntryPoints[0],
		chain)

	bundlerClient.UseModules(
		whitelistHandler,
		exp.DropExpired(),
		batch.SortByNonce(),
		batch.MaintainGasLimit(values.MaxBatchGasLimit),
		solver.ValidateIntents(),
		solver.SolveIntents(),
		simulatorHandler,
		relayer.SendUserOperation(),
		rep.IncOpsIncluded(),
		check.Clean(),
	)
	if err := bundlerClient.Run(); err != nil {
		stdLogger.Error(err, "could not run Bunclder client")
		os.Exit(1)
	}

	var debugClient = client.NewDebug(eoa, eth, mem, rep, bundlerClient, chain, values.SupportedEntryPoints[0], beneficiary)
	bundlerClient.SetMaxBatch(1)
	relayer.SetWaitTimeout(0)

	handler, shutdown := rpcHandler.NewRPCServer(values, stdLogger, relayer,
		client.NewRpcAdapter(erc4337Client, debugClient), eth, rpcClient, chain)
	defer shutdown()

	go func() {
		if err := handler.Run(fmt.Sprintf(":%d", values.Port)); err != nil {
			cancel()
			stdLogger.Error(err, "error while running HTTP Handler")
			os.Exit(1)
		}
	}()

	// Wait for the context to be canceled
	<-ctx.Done()
	log.Println("Shutting down...")
	whitelistCleanupFn()
}

func createERC4337Client(mem *mempool.Mempool, values *conf.Values, chainID *big.Int,
	ethClient *ethclient.Client, rpcClient *rpc.Client, logger logr.Logger,
	rep *entities.Reputation, validator *validations.Validator) *client.Client {

	c := client.New(mem, gas.NewDefaultOverhead(), chainID, values.SupportedEntryPoints, values.OpLookupLimit)
	c.SetGetUserOpReceiptFunc(client.GetUserOpReceiptWithEthClient(ethClient))
	c.SetGetGasPricesFunc(client.GetGasPricesWithEthClient(ethClient))
	c.SetGetGasEstimateFunc(
		client.GetGasEstimateWithEthClient(
			rpcClient,
			gas.NewDefaultOverhead(),
			chainID,
			values.MaxBatchGasLimit,
			values.NativeBundlerExecutorTracer,
		),
	)

	c.SetGetUserOpByHashFunc(client.GetUserOpByHashWithEthClient(ethClient))
	c.UseLogger(logger)

	c.UseModules(
		rep.CheckStatus(),
		rep.ValidateOpLimit(),
		validator.OpValues(),
		// Omit simulation
		rep.IncOpsSeen(),
	)

	return c
}

func createBundlerClient(mem *mempool.Mempool, chainID *big.Int,
	values *conf.Values, eth *ethclient.Client, logger logr.Logger) *bundler.Bundler {

	b := bundler.New(mem, chainID, values.SupportedEntryPoints)
	b.SetGetBaseFeeFunc(gasprice.GetBaseFeeWithEthClient(eth))
	b.SetGetGasTipFunc(gasprice.GetGasTipWithEthClient(eth))
	b.SetGetLegacyGasPriceFunc(gasprice.GetLegacyGasPriceWithEthClient(eth))
	b.UseLogger(logger)
	if err := b.UserMeter(otel.GetMeterProvider().Meter("bundler")); err != nil {
		logger.Error(err, "could not set up OTEL meter")
		os.Exit(1)
	}

	return b
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
