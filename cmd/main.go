package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

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
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/checks"
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
	stdLogger := logger.NewZeroLogr()

	relayer := srv.New(values.SupportedEntryPoints[0], eoa, eth, chain, beneficiary, stdLogger)

	println("solver URL:", values.SolverURL)
	solver := solution.New(values.SolverURL)
	if err := solution.ReportSolverHealth(values.SolverURL); err != nil {
		log.Fatal(err)
	}

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
	c.UseLogger(stdLogger)

	c.UseModules(
		rep.CheckStatus(),
		rep.ValidateOpLimit(),
		validator.OpValues(),
		// Omit simulation
		rep.IncOpsSeen(),
	)

	bundlerClient := createBundlerClient(mem, chain, values, eth, stdLogger)

	var check = (*checks.Standalone)(unsafe.Pointer(validator))
	bundlerClient.UseModules(
		exp.DropExpired(),
		batch.SortByNonce(),
		batch.MaintainGasLimit(values.MaxBatchGasLimit),
		solver.SolveIntents(),
		relayer.SendUserOperation(),
		rep.IncOpsIncluded(),
		check.Clean(),
	)
	if err := bundlerClient.Run(); err != nil {
		log.Fatal(err)
	}

	var d = client.NewDebug(eoa, eth, mem, rep, bundlerClient, chain, values.SupportedEntryPoints[0], beneficiary)
	bundlerClient.SetMaxBatch(1)
	relayer.SetWaitTimeout(0)

	handler, shutdown := rpcHandler.NewServer(values, stdLogger, relayer,
		client.NewRpcAdapter(c, d), eth, rpcClient, chain)
	defer shutdown()

	go func() {
		if err := handler.Run(fmt.Sprintf(":%d", values.Port)); err != nil {
			cancel()
			log.Fatal(err)
		}
	}()

	// Wait for the context to be canceled
	<-ctx.Done()
	log.Println("Shutting down...")
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
