package store

import (
	"context"
	"os"
	"testing"

	pb "github.com/blndgs/model/gen/go/proto/v1"
	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

// setupTestDB setup test db.
func setupTestDB(t *testing.T) (*BadgerStore, func()) {
	dir, err := os.MkdirTemp("", "badger-test-*")
	require.NoError(t, err)

	opts := badger.DefaultOptions(dir).WithLogger(nil)
	db, err := badger.Open(opts)
	require.NoError(t, err)

	store := NewBadgerStore(db, logr.Discard())

	cleanup := func() {
		db.Close()
		os.RemoveAll(dir)
	}

	return store, cleanup
}

// TestStatus test processing status.
func TestStatus(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	currentHash := "0x123"
	solvedHash := "0x456"

	t.Run("Status Not Found", func(t *testing.T) {
		_, err := store.GetStatus(ctx, currentHash)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("Update And Get Status", func(t *testing.T) {
		err := store.UpdateStatus(ctx, currentHash, solvedHash, pb.ProcessingStatus_PROCESSING_STATUS_RECEIVED)
		require.NoError(t, err)

		status, err := store.GetStatus(ctx, currentHash)
		require.NoError(t, err)
		require.Equal(t, currentHash, status.OriginalHash)
		require.Equal(t, solvedHash, status.SolvedHash)
		require.Equal(t, pb.ProcessingStatus_PROCESSING_STATUS_RECEIVED, status.Status)
	})

	t.Run("Delete Status", func(t *testing.T) {
		err := store.DeleteStatus(ctx, currentHash)
		require.NoError(t, err)

		_, err = store.GetStatus(ctx, currentHash)
		require.ErrorIs(t, err, ErrNotFound)
	})
}

// TestWhitelist test whitelisted list.
func TestWhitelist(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	addr1 := common.HexToAddress("0x123")
	addr2 := common.HexToAddress("0x456")
	addresses := []common.Address{addr1, addr2}

	t.Run("Initial Whitelist Check", func(t *testing.T) {
		isWhitelisted, err := store.IsWhitelisted(ctx, addr1)
		require.NoError(t, err)
		require.False(t, isWhitelisted)
	})

	t.Run("Add To Whitelist", func(t *testing.T) {
		err := store.AddToWhitelist(ctx, addresses)
		require.NoError(t, err)

		isWhitelisted, err := store.IsWhitelisted(ctx, addr1)
		require.NoError(t, err)
		require.True(t, isWhitelisted)

		whitelisted, err := store.GetWhitelistedAddresses(ctx)
		require.NoError(t, err)
		require.ElementsMatch(t, addresses, whitelisted)
	})

	t.Run("Remove From Whitelist", func(t *testing.T) {
		err := store.RemoveFromWhitelist(ctx, []common.Address{addr1})
		require.NoError(t, err)

		isWhitelisted, err := store.IsWhitelisted(ctx, addr1)
		require.NoError(t, err)
		require.False(t, isWhitelisted)

		isWhitelisted, err = store.IsWhitelisted(ctx, addr2)
		require.NoError(t, err)
		require.True(t, isWhitelisted)
	})
}
