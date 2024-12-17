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
	"github.com/blndgs/bundler/receipt"
	rpcHandler "github.com/blndgs/bundler/rpc"
	"github.com/blndgs/bundler/solution"
	"github.com/blndgs/bundler/srv"
	"github.com/blndgs/bundler/store"
	"github.com/blndgs/bundler/validations"
)

// build time LDFlags
var (
	CommitID, ModelVersion string
)

func main() {
	conf.SetLDFlags(CommitID, ModelVersion)
	values := conf.GetValues()

	// Initialize the logger
	stdLogger := logger.NewZeroLogr(values.DebugMode)

	// Validate the service name
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

	// Initialize the signer
	eoa, err := signer.New(values.PrivateKey)
	if err != nil {
		stdLogger.Error(err, "could not set up signer")
		os.Exit(1)
	}

	beneficiary := common.HexToAddress(values.Beneficiary)

	// Connect to the Ethereum client
	rpcClient, err := rpc.Dial(values.EthClientUrl)
	if err != nil {
		stdLogger.Error(err, "Failed to connect to Ethereum node")
		os.Exit(1)
	}

	eth := ethclient.NewClient(rpcClient)

	// Open the Badger database
	db, err := badger.Open(badger.DefaultOptions(values.DataDirectory))
	if err != nil {
		stdLogger.Error(err, "could not open badger database")
		os.Exit(1)
	}
	defer db.Close()

	// Initialize BadgerStore with logger
	store := store.NewBadgerStore(db, stdLogger)

	runDBGarbageCollection(db)

	// Initialize the mempool
	mem, err := mempool.New(db)
	if err != nil {
		stdLogger.Error(err, "could not set up Mempool")
		os.Exit(1)
	}

	// Fetch the chain ID from the Ethereum client
	chain, err := eth.ChainID(context.Background())
	if err != nil {
		stdLogger.Error(err, "could not fetch chain id from RPC")
		os.Exit(1)
	}

	// Set up alternative mempool using IPFS
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

	// Initialize the validator
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

	// Initialize expiration module
	exp := expire.New(time.Second * values.MaxOpTTL)

	// Initialize the reputation entity
	rep := entities.New(db, eth, conf.NewReputationConstantsFromEnv())

	// Initialize the relayer
	relayer := srv.New(values.SupportedEntryPoints[0], eoa, eth, chain, beneficiary, stdLogger)

	stdLogger.Info("Using solver url", "url", values.SolverURL)

	// Set up the solver
	solver := solution.New(
		values.SolverURL,
		stdLogger,
		relayer.GetOpHashes(),
		values.SupportedEntryPoints[0],
		chain,
		store)
	if err := solver.ReportSolverHealth(values.SolverURL); err != nil {
		stdLogger.Error(err, "could not verify Solver's healthcheck")
		os.Exit(1)
	}

	// Create ERC-4337 client
	erc4337Client := createERC4337Client(mem, values, chain, eth, rpcClient, stdLogger, rep, validator, store)

	// Create bundler client
	bundlerClient := createBundlerClient(mem, chain, values, eth, stdLogger)

	check := validator.ToStandaloneCheck()

	// Set up whitelist handler
	whitelistHandler, whitelistCleanupFn := srv.CheckSenderWhitelist(
		store,
		values.WhiteListedAddresses,
		stdLogger,
		relayer.GetOpHashes(),
		values.SupportedEntryPoints[0],
		chain)

	if whitelistHandler == nil {
		err := "could not set up sender whitelist middleware"
		stdLogger.Info(err, "whitelisting handler could not be setup")
		os.Exit(1)
	}

	// Set up transaction simulation handler
	simulatorHandler := srv.SimulateTxWithTenderly(
		eoa,
		values,
		eth,
		stdLogger,
		relayer.GetOpHashes(),
		values.SupportedEntryPoints[0],
		chain)

	// Add modules to the bundler client
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
		stdLogger.Error(err, "could not run Bundler client")
		os.Exit(1)
	}

	// Set up debug client and RPC server
	var debugClient = client.NewDebug(eoa, eth, mem, rep, bundlerClient, chain, values.SupportedEntryPoints[0], beneficiary)
	bundlerClient.SetMaxBatch(1)
	relayer.SetWaitTimeout(0)
	rpcAdaptor := rpcHandler.NewRpcAdapter(erc4337Client, debugClient)
	handler, shutdown := rpcHandler.NewRPCServer(values, stdLogger, relayer,
		rpcAdaptor, eth, rpcClient, chain)
	defer shutdown()

	// Start the RPC handler
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

// createERC4337Client initializes the ERC-4337 client with configured modules and custom functions.
func createERC4337Client(
	mem *mempool.Mempool,
	values *conf.Values,
	chainID *big.Int,
	ethClient *ethclient.Client,
	rpcClient *rpc.Client,
	logger logr.Logger,
	rep *entities.Reputation,
	validator *validations.Validator,
	store *store.BadgerStore) *rpcHandler.Client {

	c := rpcHandler.NewClient(chainID, mem, values, gas.NewDefaultOverhead())

	// Configure custom receipt and gas-related functions
	c.SetGetUserOpReceiptFunc(receipt.GetUserOpReceiptWithEthClient(ethClient, store))
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

	// Add reputation and validation modules
	c.UseModules(
		rep.CheckStatus(),
		rep.ValidateOpLimit(),
		validator.OpValues(),
		// Omit simulation
		rep.IncOpsSeen(),
	)

	return c
}

// createBundlerClient initializes the bundler client with necessary configurations and modules.
func createBundlerClient(
	mem *mempool.Mempool,
	chainID *big.Int,
	values *conf.Values,
	eth *ethclient.Client,
	logger logr.Logger) *bundler.Bundler {

	b := bundler.New(mem, chainID, values.SupportedEntryPoints)

	// Configure gas price functions
	b.SetGetBaseFeeFunc(gasprice.GetBaseFeeWithEthClient(eth))
	b.SetGetGasTipFunc(gasprice.GetGasTipWithEthClient(eth))
	b.SetGetLegacyGasPriceFunc(gasprice.GetLegacyGasPriceWithEthClient(eth))
	b.UseLogger(logger)

	// Set up OpenTelemetry meter
	if err := b.UserMeter(otel.GetMeterProvider().Meter("bundler")); err != nil {
		logger.Error(err, "could not set up OTEL meter")
		os.Exit(1)
	}

	return b
}

// runDBGarbageCollection periodically runs garbage collection on the Badger database to manage value log size.
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
