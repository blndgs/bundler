package conf

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/spf13/viper"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules/entities"
	"github.com/stackup-wallet/stackup-bundler/pkg/signer"
)

const (
	CollectorTracer           = "bundlerCollectorTracer"
	ExecutorTracer            = "bundlerExecutorTracer"
	DefaultBundlerServiceName = "bundler"
	DataDir                   = "/tmp/balloondogs_db"
	EntrypointAddrV060        = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"
	OpLookupLimit             = 2000
	MaxBatchGasLimit          = 12000000
	MaxVerificationGas        = 6000000
	MaxTTLSeconds             = 180
)

type Values struct {
	PrivateKey                   string
	EthClientUrl                 string
	Port                         int
	DataDirectory                string
	SupportedEntryPoints         []common.Address
	Beneficiary                  string
	NativeBundlerCollectorTracer string
	NativeBundlerExecutorTracer  string
	MaxVerificationGas           *big.Int
	MaxBatchGasLimit             *big.Int
	MaxOpTTL                     time.Duration
	OpLookupLimit                uint64
	ReputationConstants          *entities.ReputationConstants
	EthBuilderUrls               []string
	BlocksInTheFuture            int
	StatusTimeout                time.Duration
	OTELIsEnabled                bool
	OTELServiceName              string
	OTELCollectorHeaders         map[string]string
	OTELCollectorUrl             string
	OTELInsecureMode             bool
	AltMempoolIPFSGateway        string
	AltMempoolIds                []string
	IsOpStackNetwork             bool
	IsRIP7212Supported           bool
	DebugMode                    bool
	GinMode                      string
	SolverURL                    string
}

func variableNotSetOrIsNil(env string) bool {
	return !viper.IsSet(env) || viper.GetString(env) == ""
}

func envKeyValStringToMap(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, "&") {
		kv := strings.Split(pair, "=")
		if len(kv) != 2 {
			break
		}
		out[kv[0]] = kv[1]
	}
	return out
}

func envArrayToAddressSlice(s string) []common.Address {
	env := strings.Split(s, ",")
	slc := []common.Address{}
	for _, ep := range env {
		slc = append(slc, common.HexToAddress(strings.TrimSpace(ep)))
	}

	return slc
}

func envArrayToStringSlice(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func GetValues() *Values {
	// Default variables
	viper.SetDefault("erc4337_bundler_port", 4337)
	viper.SetDefault("erc4337_bundler_data_directory", DataDir)
	viper.SetDefault("erc4337_bundler_supported_entry_points", EntrypointAddrV060)
	viper.SetDefault("erc4337_bundler_max_verification_gas", MaxVerificationGas)
	viper.SetDefault("erc4337_bundler_max_batch_gas_limit", MaxBatchGasLimit)
	viper.SetDefault("erc4337_bundler_max_op_ttl_seconds", MaxTTLSeconds)
	viper.SetDefault("erc4337_bundler_op_lookup_limit", OpLookupLimit)
	viper.SetDefault("erc4337_bundler_blocks_in_the_future", 6)
	viper.SetDefault("erc4337_bundler_otel_insecure_mode", false)
	viper.SetDefault("erc4337_bundler_is_op_stack_network", false)
	viper.SetDefault("erc4337_bundler_is_rip7212_supported", false)
	viper.SetDefault("erc4337_bundler_debug_mode", true)
	viper.SetDefault("erc4337_bundler_gin_mode", gin.ReleaseMode)
	viper.SetDefault("erc4337_bundler_native_bundler_collector_tracer", CollectorTracer)
	viper.SetDefault("erc4337_bundler_native_bundler_executor_tracer", ExecutorTracer)
	viper.SetDefault("erc4337_bundler_status_timeout", time.Second*300)

	// Read in from .env file if available
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		// config file is optional
		if !errors.As(err, &configFileNotFoundError) {
			panic(fmt.Errorf("fatal error reading config file: %w", err))
		}
		// use env vars instead
	}

	// Read in from environment variables
	_ = viper.BindEnv("erc4337_bundler_eth_client_url")
	_ = viper.BindEnv("erc4337_bundler_private_key")
	_ = viper.BindEnv("erc4337_bundler_port")
	_ = viper.BindEnv("erc4337_bundler_data_directory")
	_ = viper.BindEnv("erc4337_bundler_supported_entry_points")
	_ = viper.BindEnv("erc4337_bundler_beneficiary")
	_ = viper.BindEnv("erc4337_bundler_native_bundler_collector_tracer")
	_ = viper.BindEnv("erc4337_bundler_native_bundler_executor_tracer")
	_ = viper.BindEnv("erc4337_bundler_max_verification_gas")
	_ = viper.BindEnv("erc4337_bundler_max_batch_gas_limit")
	_ = viper.BindEnv("erc4337_bundler_max_op_ttl_seconds")
	_ = viper.BindEnv("erc4337_bundler_op_lookup_limit")
	_ = viper.BindEnv("erc4337_bundler_eth_builder_urls")
	_ = viper.BindEnv("erc4337_bundler_blocks_in_the_future")
	_ = viper.BindEnv("erc4337_bundler_otel_is_enabled")
	_ = viper.BindEnv("erc4337_bundler_otel_service_name")
	_ = viper.BindEnv("erc4337_bundler_otel_collector_headers")
	_ = viper.BindEnv("erc4337_bundler_otel_collector_url")
	_ = viper.BindEnv("erc4337_bundler_otel_insecure_mode")
	_ = viper.BindEnv("erc4337_bundler_alt_mempool_ipfs_gateway")
	_ = viper.BindEnv("erc4337_bundler_alt_mempool_ids")
	_ = viper.BindEnv("erc4337_bundler_is_op_stack_network")
	_ = viper.BindEnv("erc4337_bundler_is_rip7212_supported")
	_ = viper.BindEnv("erc4337_bundler_debug_mode")
	_ = viper.BindEnv("erc4337_bundler_gin_mode")
	_ = viper.BindEnv("solver_url")
	_ = viper.BindEnv("erc4337_bundler_status_timeout")

	// Validate required variables
	if variableNotSetOrIsNil("erc4337_bundler_eth_client_url") {
		panic("Fatal config error: erc4337_bundler_eth_client_url not set")
	}

	if variableNotSetOrIsNil("erc4337_bundler_private_key") {
		panic("Fatal config error: erc4337_bundler_private_key not set")
	}

	if !viper.IsSet("erc4337_bundler_beneficiary") {
		s, err := signer.New(viper.GetString("erc4337_bundler_private_key"))
		if err != nil {
			panic(err)
		}
		viper.SetDefault("erc4337_bundler_beneficiary", s.Address.String())
	}

	switch viper.GetString("mode") {
	case "searcher":
		if variableNotSetOrIsNil("erc4337_bundler_eth_builder_urls") {
			panic("Fatal config error: erc4337_bundler_eth_builder_urls not set")
		}
	}

	// Validate O11Y variables
	if viper.IsSet("erc4337_bundler_otel_service_name") &&
		variableNotSetOrIsNil("erc4337_bundler_otel_collector_url") {
		panic("Fatal config error: erc4337_bundler_otel_service_name is set without a collector URL")
	}

	// Validate Alternative mempool variables
	if viper.IsSet("erc4337_bundler_alt_mempool_ids") &&
		variableNotSetOrIsNil("erc4337_bundler_alt_mempool_ipfs_gateway") {
		panic("Fatal config error: erc4337_bundler_alt_mempool_ids is set without specifying an IPFS gateway")
	}

	if variableNotSetOrIsNil("solver_url") && !strings.Contains(viper.GetString("solver_url"), "/solve") {
		panic("Fatal config error: solver_url not set")
	}

	// Return Values
	privateKey := viper.GetString("erc4337_bundler_private_key")
	ethClientUrl := viper.GetString("erc4337_bundler_eth_client_url")
	port := viper.GetInt("erc4337_bundler_port")
	dataDirectory := viper.GetString("erc4337_bundler_data_directory")
	supportedEntryPoints := envArrayToAddressSlice(viper.GetString("erc4337_bundler_supported_entry_points"))
	beneficiary := viper.GetString("erc4337_bundler_beneficiary")
	nativeBundlerCollectorTracer := viper.GetString("erc4337_bundler_native_bundler_collector_tracer")
	nativeBundlerExecutorTracer := viper.GetString("erc4337_bundler_native_bundler_executor_tracer")
	maxVerificationGas := big.NewInt(int64(viper.GetInt("erc4337_bundler_max_verification_gas")))
	maxBatchGasLimit := big.NewInt(int64(viper.GetInt("erc4337_bundler_max_batch_gas_limit")))
	maxOpTTL := time.Second * viper.GetDuration("erc4337_bundler_max_op_ttl_seconds")
	opLookupLimit := viper.GetUint64("erc4337_bundler_op_lookup_limit")
	ethBuilderUrls := envArrayToStringSlice(viper.GetString("erc4337_bundler_eth_builder_urls"))
	blocksInTheFuture := viper.GetInt("erc4337_bundler_blocks_in_the_future")
	otelIsEnabled := viper.GetBool("erc4337_bundler_otel_is_enabled")
	otelServiceName := viper.GetString("erc4337_bundler_otel_service_name")
	otelCollectorHeader := envKeyValStringToMap(viper.GetString("erc4337_bundler_otel_collector_headers"))
	otelCollectorUrl := viper.GetString("erc4337_bundler_otel_collector_url")
	otelInsecureMode := viper.GetBool("erc4337_bundler_otel_insecure_mode")
	altMempoolIPFSGateway := viper.GetString("erc4337_bundler_alt_mempool_ipfs_gateway")
	altMempoolIds := envArrayToStringSlice(viper.GetString("erc4337_bundler_alt_mempool_ids"))
	isOpStackNetwork := viper.GetBool("erc4337_bundler_is_op_stack_network")
	isRIP7212Supported := viper.GetBool("erc4337_bundler_is_rip7212_supported")
	debugMode := viper.GetBool("erc4337_bundler_debug_mode")
	ginMode := viper.GetString("erc4337_bundler_gin_mode")
	solverURL := viper.GetString("solver_url")
	useropStatusWaitTime := viper.GetDuration("erc4337_bundler_status_timeout")

	return &Values{
		PrivateKey:                   privateKey,
		EthClientUrl:                 ethClientUrl,
		Port:                         port,
		DataDirectory:                dataDirectory,
		SupportedEntryPoints:         supportedEntryPoints,
		Beneficiary:                  beneficiary,
		NativeBundlerCollectorTracer: nativeBundlerCollectorTracer,
		NativeBundlerExecutorTracer:  nativeBundlerExecutorTracer,
		MaxVerificationGas:           maxVerificationGas,
		MaxBatchGasLimit:             maxBatchGasLimit,
		MaxOpTTL:                     maxOpTTL,
		OpLookupLimit:                opLookupLimit,
		ReputationConstants:          NewReputationConstantsFromEnv(),
		EthBuilderUrls:               ethBuilderUrls,
		BlocksInTheFuture:            blocksInTheFuture,
		OTELIsEnabled:                otelIsEnabled,
		OTELServiceName:              otelServiceName,
		OTELCollectorHeaders:         otelCollectorHeader,
		OTELCollectorUrl:             otelCollectorUrl,
		OTELInsecureMode:             otelInsecureMode,
		AltMempoolIPFSGateway:        altMempoolIPFSGateway,
		AltMempoolIds:                altMempoolIds,
		IsOpStackNetwork:             isOpStackNetwork,
		IsRIP7212Supported:           isRIP7212Supported,
		DebugMode:                    debugMode,
		GinMode:                      ginMode,
		SolverURL:                    solverURL,
		StatusTimeout:                useropStatusWaitTime,
	}
}

func NewReputationConstantsFromEnv() *entities.ReputationConstants {
	return &entities.ReputationConstants{
		MinUnstakeDelay:                86400,
		MinStakeValue:                  2000000000000000,
		SameSenderMempoolCount:         4,
		SameUnstakedEntityMempoolCount: 11,
		ThrottledEntityMempoolCount:    4,
		ThrottledEntityLiveBlocks:      10,
		ThrottledEntityBundleCount:     4,
		MinInclusionRateDenominator:    10,
		ThrottlingSlack:                10,
		BanSlack:                       50,
	}
}
