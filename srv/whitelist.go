package srv

import (
	"context"
	"fmt"

	"github.com/blndgs/bundler/utils"
	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
)

func getWhitelistKey(addr common.Address) string {
	return fmt.Sprintf("bundler-whitelist-%s", addr.Hex())
}

func CheckSenderWhitelist(db *badger.DB, 
whitelistedAddresses []common.Address, 
logger logr.Logger) modules.BatchHandlerFunc {
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
		return nil
	}

	return func(ctx *modules.BatchHandlerCtx) error {

		_, span := utils.GetTracer().
			Start(context.Background(), "WithAdressWhitelist")
		defer span.End()

		if len(whitelistedAddresses) == 0 {
			logger.Info("Address whitelisting not enabled. Skipping middleware.")
			return nil
		}

		for _, userop := range ctx.Batch {
			senderKey := getWhitelistKey(userop.Sender)
			err := db.View(func(txn *badger.Txn) error {
				_, err := txn.Get([]byte(senderKey))
				return err
			})
			if err != nil {
				logger.Error(err, "Sender not found in whitelist; cannot execute transaction", "address", userop.Sender.Hex())
				return err
			}
		}
		return nil
	}
}
