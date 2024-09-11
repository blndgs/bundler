//go:build integration
// +build integration

package srv

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"os"
	"testing"

	"github.com/blndgs/bundler/conf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
	"github.com/stretchr/testify/require"
)

func TestSimulateTx(t *testing.T) {
	t.Skip()

	rpcURL := os.Getenv("TEST_RPC_URL")

	require.NotEmpty(t, rpcURL)

	client, err := ethclient.Dial(rpcURL)
	require.NoError(t, err)

	hashes := xsync.NewMapOf[string, OpHashes]()

	cfg := &conf.Values{
		SimulationEnabled: true,
		EthClientUrl:      rpcURL,
	}

	handler := SimulateTxWithTenderly(cfg, client, logr.Discard(),
		hashes,
		common.HexToAddress(conf.EntrypointAddrV060), big.NewInt(1))

	batch := []*userop.UserOperation{}

	b, err := hex.DecodeString(`d6f6b170000000000000000000000000000000000000000000000000000000000000006000000000000000000000000000000000000000000000000000000000000000c0000000000000000000000000000000000000000000000000000000000000012000000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000020000000000000000000000006b175474e89094c44da98b954eedeac495271d0f000000000000000000000000def1c0ded9bec7f1a1670819833240f027b25eff0000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000c00000000000000000000000000000000000000000000000000000000000000044095ea7b3000000000000000000000000def1c0ded9bec7f1a1670819833240f027b25eff0000000000000000000000000000000000000000000000000de0b6b3a7640000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000748415565b00000000000000000000000006b175474e89094c44da98b954eedeac495271d0f000000000000000000000000a0b86991c6218b36c1d19d4a2e9eb0ce3606eb480000000000000000000000000000000000000000000000000de0b6b3a764000000000000000000000000000000000000000000000000000000000000000da8ae00000000000000000000000000000000000000000000000000000000000000a00000000000000000000000000000000000000000000000000000000000000003000000000000000000000000000000000000000000000000000000000000006000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000500000000000000000000000000000000000000000000000000000000000000002100000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000340000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000000000000000000000000000006b175474e89094c44da98b954eedeac495271d0f000000000000000000000000a0b86991c6218b36c1d19d4a2e9eb0ce3606eb4800000000000000000000000000000000000000000000000000000000000001400000000000000000000000000000000000000000000000000000000000000300000000000000000000000000000000000000000000000000000000000000030000000000000000000000000000000000000000000000000000000000000002c00000000000000000000000000000000000000000000000000de0b6b3a7640000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000003000000000000000000000000000000000000000000000000000000000000000001000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000002556e69737761705632000000000000000000000000000000000000000000000000000000000000000de0b6b3a764000000000000000000000000000000000000000000000000000000000000000dadee000000000000000000000000000000000000000000000000000000000000008000000000000000000000000000000000000000000000000000000000000000a0000000000000000000000000f164fc0ec4e93095b804a4795bbe1e041497b92a000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000020000000000000000000000006b175474e89094c44da98b954eedeac495271d0f000000000000000000000000a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001b000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000a000000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000001000000000000000000000000a0b86991c6218b36c1d19d4a2e9eb0ce3606eb480000000000000000000000000000000000000000000000000000000000000540000000000000000000000000ad01c20d5886137e056775af56915de824c8fce5000000000000000000000000000000000000000000000000000000000000001c000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000e00000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000a000000000000000000000000000000000000000000000000000000000000000020000000000000000000000006b175474e89094c44da98b954eedeac495271d0f000000000000000000000000eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee0000000000000000000000000000000000000000000000000000000000000000869584cd000000000000000000000000100000000000000000000000000000000000001100000000000000000000000000000000000000006f5500e2be6972e25053af40000000000000000000000000000000000000000000000000`)
	require.NoError(t, err)

	// build multiple user ops
	batch = append(batch, &userop.UserOperation{
		Sender:               common.HexToAddress("0xc291efdc1a6420cbb226294806604833982ed24d"),
		MaxFeePerGas:         big.NewInt(1000),
		Nonce:                big.NewInt(20),
		CallGasLimit:         big.NewInt(1000),
		PreVerificationGas:   big.NewInt(1002),
		VerificationGasLimit: big.NewInt(10005),
		MaxPriorityFeePerGas: big.NewInt(14003),
		CallData:             b,
	})

	composedHandler := modules.ComposeBatchHandlerFunc(handler)

	require.NoError(t, composedHandler(&modules.BatchHandlerCtx{
		Batch: batch,
	}))
}

func TestSimulateTx_NotEnabled(t *testing.T) {

	rpcURL := os.Getenv("TEST_RPC_URL")

	require.NotEmpty(t, rpcURL)

	client, err := ethclient.Dial(rpcURL)
	require.NoError(t, err)

	hashes := xsync.NewMapOf[string, OpHashes]()

	cfg := &conf.Values{
		SimulationEnabled: false,
		EthClientUrl:      rpcURL,
	}

	var logBuffer = bytes.NewBuffer(nil)

	zl := zerolog.New(logBuffer).
		With().
		Caller().
		Timestamp().
		Logger().
		Level(zerolog.InfoLevel)

	handler := SimulateTxWithTenderly(cfg, client, zerologr.New(&zl),
		hashes,
		common.HexToAddress(conf.EntrypointAddrV060), big.NewInt(1))

	batch := []*userop.UserOperation{}

	// build multiple user ops
	batch = append(batch, &userop.UserOperation{
		Sender:               common.HexToAddress("0xc291efdc1a6420cbb226294806604833982ed24d"),
		MaxFeePerGas:         big.NewInt(1000),
		Nonce:                big.NewInt(20),
		CallGasLimit:         big.NewInt(1000),
		PreVerificationGas:   big.NewInt(1002),
		VerificationGasLimit: big.NewInt(10005),
		MaxPriorityFeePerGas: big.NewInt(14003),
		CallData:             []byte(``),
	})

	composedHandler := modules.ComposeBatchHandlerFunc(handler)

	require.NoError(t, composedHandler(&modules.BatchHandlerCtx{
		Batch: batch,
	}))

	require.Contains(t, logBuffer.String(), "skipping simulation")
}
