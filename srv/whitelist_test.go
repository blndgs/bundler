package srv

import (
	"math/big"
	"testing"

	"github.com/blndgs/bundler/conf"
	"github.com/blndgs/bundler/store"
	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
	"github.com/stretchr/testify/require"
)

func TestCheckSenderWhitelist(t *testing.T) {

	tt := []struct {
		name                 string
		whitelistedAddresses []string
		addressToUseInTx     []string
		expectedBatchNumber  int
	}{
		{
			name:                 "no whitelisted address",
			whitelistedAddresses: []string{},
			addressToUseInTx:     []string{"0x000000000000000000000000000000000000dEaD"},
			expectedBatchNumber:  1,
		},
		{
			name:                 "whitelisted address and matches",
			whitelistedAddresses: []string{"0x000000000000000000000000000000000000dEaD"},
			addressToUseInTx:     []string{"0x000000000000000000000000000000000000dEaD"},
			expectedBatchNumber:  1,
		},
		{
			name:                 "whitelisted address but does not match",
			whitelistedAddresses: []string{"0x000000000000000000000000000000000000dEaD"},
			addressToUseInTx:     []string{"0xdAC17F958D2ee523a2206206994597C13D831ec7"},
			expectedBatchNumber:  0,
		},
		{
			name:                 "allowed and non allowed addresses",
			whitelistedAddresses: []string{"0x000000000000000000000000000000000000dEaD", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"},
			// only 1 address allowed
			addressToUseInTx:    []string{"0xdAC17F958D2ee523a2206206994597C13D831ec7", "0x000000000000000000000000000000000000dEaD"},
			expectedBatchNumber: 1,
		},
		{
			name:                 "all whitelisted addresses",
			whitelistedAddresses: []string{"0x000000000000000000000000000000000000dEaD", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"},
			addressToUseInTx:     []string{"0x000000000000000000000000000000000000dEaD", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"},
			expectedBatchNumber:  2,
		},
	}

	for _, v := range tt {

		t.Run(v.name, func(t *testing.T) {
			db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true))
			require.NoError(t, err)
			store := store.NewBadgerStore(db, logr.Discard())
			defer func() {
				require.NoError(t, db.Close())
			}()

			whitelistedAddresses := make([]common.Address, len(v.whitelistedAddresses))

			for _, addr := range v.whitelistedAddresses {
				whitelistedAddresses = append(whitelistedAddresses, common.HexToAddress(addr))
			}

			handler, teardownFn := CheckSenderWhitelist(store, whitelistedAddresses, logr.Discard(),
				xsync.NewMapOf[string, OpHashes](),
				common.HexToAddress(conf.EntrypointAddrV060), big.NewInt(1))
			defer teardownFn()

			batch := []*userop.UserOperation{}

			// build multiple user ops
			for _, addr := range v.addressToUseInTx {
				batch = append(batch, &userop.UserOperation{
					Sender:               common.HexToAddress(addr),
					MaxFeePerGas:         big.NewInt(1000),
					Nonce:                big.NewInt(20),
					CallGasLimit:         big.NewInt(1000),
					PreVerificationGas:   big.NewInt(1002),
					VerificationGasLimit: big.NewInt(10005),
					MaxPriorityFeePerGas: big.NewInt(14003),
				})
			}

			composedHandler := modules.ComposeBatchHandlerFunc(handler, func(ctx *modules.BatchHandlerCtx) error {
				// handler to verify the batch has been modified based off the whitelist
				require.Len(t, ctx.Batch, v.expectedBatchNumber)
				return nil
			})

			require.NoError(t, composedHandler(&modules.BatchHandlerCtx{
				Batch: batch,
			}))

		})
	}
}
