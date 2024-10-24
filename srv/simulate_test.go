package srv

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blndgs/bundler/conf"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/signer"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/sha3"
)

func TestSimulateTx(t *testing.T) {

	rpcURL := os.Getenv("TEST_RPC_URL")

	require.NotEmpty(t, rpcURL)

	client, err := ethclient.Dial(rpcURL)
	require.NoError(t, err)

	hashes := xsync.NewMapOf[string, OpHashes]()

	eoaSigner := generateWallet(t)

	// Ankr staking
	b, err := hex.DecodeString(`d6f6b170000000000000000000000000000000000000000000000000000000000000006000000000000000000000000000000000000000000000000000000000000000a000000000000000000000000000000000000000000000000000000000000000e00000000000000000000000000000000000000000000000000000000000000001000000000000000000000000000000000000000000000000016345785d8a0006000000000000000000000000000000000000000000000000000000000000000100000000000000000000000084db6ee82b7cf3b47e8f19270abde5718b9366700000000000000000000000000000000000000000000000000000000000000001000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000049fa65c5600000000000000000000000000000000000000000000000000000000`)
	require.NoError(t, err)

	sender := getSender(t, eoaSigner.Address, rpcURL)

	op := &userop.UserOperation{
		Sender:               sender,
		Nonce:                big.NewInt(0),
		CallData:             b,
		InitCode:             getInitCode(t, eoaSigner.Address),
		CallGasLimit:         big.NewInt(200000),
		PreVerificationGas:   big.NewInt(500000),
		VerificationGasLimit: big.NewInt(500000),
		MaxFeePerGas:         big.NewInt(200000),
		MaxPriorityFeePerGas: big.NewInt(200000),
	}

	op, err = sign(big.NewInt(888), eoaSigner.PrivateKey, op, common.HexToAddress(conf.EntrypointAddrV060))
	require.NoError(t, err)

	beneficiary := common.HexToAddress("0xa4BFe126D3aD137F972695dDdb1780a29065e556")

	cfg := &conf.Values{
		SimulationEnabled: true,
		EthClientUrl:      rpcURL,
		Beneficiary:       eoaSigner.Address.Hex(),
		SimulationURL:     rpcURL,
	}

	fundUserWallet(t, sender, rpcURL)

	handler := SimulateTxWithTenderly(
		&signer.EOA{
			Address: beneficiary,
		},
		cfg, client,
		logr.Discard(),
		hashes,
		common.HexToAddress(conf.EntrypointAddrV060),
		big.NewInt(1))

	composedHandler := modules.ComposeBatchHandlerFunc(handler)

	require.NoError(t, composedHandler(&modules.BatchHandlerCtx{
		Batch: []*userop.UserOperation{op},
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

	handler := SimulateTxWithTenderly(
		generateWallet(t),
		cfg, client,
		zerologr.New(&zl),
		hashes,
		common.HexToAddress(conf.EntrypointAddrV060),
		big.NewInt(1))

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

func generateWallet(t *testing.T) *signer.EOA {
	privateKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	require.True(t, ok)

	publicKeyBytes := crypto.FromECDSAPub(publicKeyECDSA)

	hash := sha3.NewLegacyKeccak256()
	_, err = hash.Write(publicKeyBytes[1:])
	require.NoError(t, err)

	s, err := signer.New(fmt.Sprintf("%x", privateKey.D.Bytes()))
	require.NoError(t, err)

	return s
}

func fundUserWallet(t *testing.T,
	addr common.Address,
	rpcURL string) error {

	reqbody := fmt.Sprintf(`
{
    "jsonrpc": "2.0",
    "method": "tenderly_setBalance",
    "params": [
      [
        "%s"
        ],
      "0x3635C9ADC5DEA00000"
      ]
}
	`, addr.Hex())

	req, err := http.NewRequest(http.MethodPost, rpcURL, strings.NewReader(reqbody))
	require.NoError(t, err)

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")

	client := &http.Client{
		Timeout: time.Minute,
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)

	return nil
}

func sign(chainID *big.Int,
	privateKey *ecdsa.PrivateKey,
	userOp *userop.UserOperation,
	entryPointAddr common.Address) (*userop.UserOperation, error) {

	signature, err := getSignature(chainID, privateKey, entryPointAddr, userOp)
	if err != nil {
		return &userop.UserOperation{}, err
	}

	userOp.Signature = signature
	return userOp, nil
}

func getSignature(chainID *big.Int, privateKey *ecdsa.PrivateKey, entryPointAddr common.Address,
	userOp *userop.UserOperation) ([]byte, error) {
	userOpHashObj := userOp.GetUserOpHash(entryPointAddr, chainID)

	userOpHash := userOpHashObj.Bytes()
	prefixedHash := crypto.Keccak256Hash(
		[]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(userOpHash), userOpHash)),
	)

	signature, err := crypto.Sign(prefixedHash.Bytes(), privateKey)
	if err != nil {
		return nil, err
	}

	signature[64] += 27
	return signature, nil
}

func getSender(t *testing.T, account common.Address, rpcURL string) common.Address {

	parsed, err := abi.JSON(strings.NewReader(`
[
  {
    "constant": false,
    "inputs": [
      {
        "type": "address"
      },
		{
		"type" : "uint256"
		}
    ],
    "name": "getAddress",
    "outputs": [
      {
        "type": "address"
      }
    ],
    "payable": false,
    "type": "function"
  }
]
		`))
	require.NoError(t, err)

	callData, err := parsed.Pack("getAddress", account, big.NewInt(0))
	require.NoError(t, err)

	client, err := ethclient.Dial(rpcURL)
	require.NoError(t, err)

	to := common.HexToAddress("0x61e218301932a2550ae8e4cd1ecfca7be64e57dc")

	result, err := client.CallContract(context.Background(), ethereum.CallMsg{
		To:   &to,
		Data: callData,
	}, nil)
	require.NoError(t, err)

	addr := common.Address{}
	err = parsed.UnpackIntoInterface(&addr, "getAddress", result)
	require.NoError(t, err)

	return addr
}

func getInitCode(t *testing.T,
	addr common.Address) []byte {
	t.Helper()

	s := fmt.Sprintf(`0x61e218301932a2550AE8E4Cd1EcfCA7bE64E57DC5fbfb9cf000000000000000000000000%s0000000000000000000000000000000000000000000000000000000000000000`,
		strings.TrimPrefix(addr.Hex(), "0x"))

	hexStr := strings.TrimPrefix(s, "0x")

	b, err := hex.DecodeString(hexStr)
	require.NoError(t, err)

	return b
}
