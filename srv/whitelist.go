package srv

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/blndgs/bundler/utils"
	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
)

func getWhitelistKey(addr common.Address) string {
	return fmt.Sprintf("whitelist-%s", addr.Hex())
}

// CheckSenderWhitelist is an handler that checks to limit on-chain userops to a set of
// addresses. These addresses must be initialized on start up and they must be a comma seperated list
// ERC4337_BUNDLER_ADDRESS_WHITELIST=0xAddress1,0xAddress2
// If not provided, this middleware will always act as a no-op
//
// It also returns a function that acts as a clean up operation
// We do not want to persist whitelisted addresses between restarts
// They must always come fresh from the env variable
func CheckSenderWhitelist(db *badger.DB,
	whitelistedAddresses []common.Address,
	logger logr.Logger, txHashes *xsync.MapOf[string, OpHashes],
	entrypointAddr common.Address, chainID *big.Int) (modules.BatchHandlerFunc, func() error) {

	logger.Info("Setting up addresses in BadgerDB for whitelist checks")

	if len(whitelistedAddresses) > 0 {
		logger.Info("Bundler address whitelisting enabled", "number_of_whitelisted_addresses", len(whitelistedAddresses))
	}

	// Use a single transaction to store all addresses
	err := db.Update(func(txn *badger.Txn) error {
		for _, addr := range whitelistedAddresses {
			if err := txn.Set([]byte(getWhitelistKey(addr)), addr.Bytes()); err != nil {
				logger.Error(err, "Failed to store address in DB for whitelisting", "address", addr.Hex())
				return err
			}

			logger.Info("Address stored in DB whitelist", "address", addr.Hex())
		}
		return nil
	})

	if err != nil {
		logger.Error(err, "Failed to store multiple addresses in BadgerDB")
		return nil, func() error { return nil }
	}

	return func(ctx *modules.BatchHandlerCtx) error {

			_, span := utils.GetTracer().
				Start(context.Background(), "WithAdressWhitelist")
			defer span.End()

			if len(whitelistedAddresses) == 0 {
				logger.Info("Address whitelisting not enabled. Skipping handler")
				return nil
			}

			for idx, userop := range ctx.Batch {
				senderKey := getWhitelistKey(userop.Sender)
				err := db.View(func(txn *badger.Txn) error {
					_, err := txn.Get([]byte(senderKey))
					return err
				})
				if err != nil {
					logger.Error(err, "Sender not found in whitelist; removing transaction from batch", "address", userop.Sender.Hex())
					ctx.MarkOpIndexForRemoval(idx, "sender not whitelisted")

					currentOpHash, unsolvedOpHash := utils.GetUserOpHash(userop, entrypointAddr, chainID)

					txHashes.Compute(unsolvedOpHash, func(oldValue OpHashes, loaded bool) (newValue OpHashes, delete bool) {
						return OpHashes{
							Error:  errors.Join(oldValue.Error, errors.New("sender not found in whitelist")),
							Solved: currentOpHash,
						}, false
					})
				}
			}

			return nil
		}, func() error {

			return db.Update(func(txn *badger.Txn) error {
				for _, addr := range whitelistedAddresses {
					if err := txn.Delete([]byte(getWhitelistKey(addr))); err != nil {
						logger.Error(err, "Failed to delete address in DB for whitelisting", "address", addr.Hex())
						return err
					}
				}
				return nil
			})
		}
}
