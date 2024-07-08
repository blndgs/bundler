package srv

import (
	"log"
	"testing"

	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
	"github.com/stackup-wallet/stackup-bundler/pkg/userop"
	"github.com/stretchr/testify/require"
)

func TestCheckSenderWhitelist(t *testing.T) {

	db, err := badger.Open(badger.DefaultOptions("testdata/test.db").WithInMemory(true))
	if err != nil {
		log.Fatal(err)
	}

	defer func() {
		require.NoError(t, db.Close())
	}()

	handler := CheckSenderWhitelist(db, []common.Address{
		common.HexToAddress("0x000000000000000000000000000000000000dEaD"),
	}, logr.Discard())

	require.NoError(t, handler(&modules.BatchHandlerCtx{
		Batch: []*userop.UserOperation{
			{
				Sender: common.HexToAddress("0x000000000000000000000000000000000000dEaD"),
			},
		},
	}))
}

func TestCheckSenderWhitelist_AddressNotWhitelisted(t *testing.T) {

	db, err := badger.Open(badger.DefaultOptions("testdata/test.db").WithInMemory(true))
	if err != nil {
		log.Fatal(err)
	}

	// empty whitelist
	handler := CheckSenderWhitelist(db, []common.Address{}, logr.Discard())

	require.NoError(t, handler(&modules.BatchHandlerCtx{
		Batch: []*userop.UserOperation{
			{
				Sender: common.HexToAddress("0x000000000000000000000000000000000000dEaD"),
			},
		},
	}))
}
