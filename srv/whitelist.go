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
	whitelistedAddress []common.Address,
	logger logr.Logger) modules.BatchHandlerFunc {

	logger.Info("setting addresses into badger for whitelisting checks")

	if len(whitelistedAddress) > 0 {
		logger.Info("Bundler address whitelisting is enabled",
			"number_of_whitelisted_addresses", len(whitelistedAddress))
	}

	for _, addr := range whitelistedAddress {
		err := db.Update(func(txn *badger.Txn) error {
			return txn.Set([]byte(getWhitelistKey(addr)), addr.Bytes())
		})
		if err != nil {
			logger.Error(err, "could not store address in db for whitelisting",
				"address", addr.Hex())
			panic("could not store address in Badger for whitelisting checks")
		}

		logger.Info("address stored in cache whitelist", "address", addr.Hex())
	}

	return func(ctx *modules.BatchHandlerCtx) error {

		_, span := utils.GetTracer().
			Start(context.Background(), "WithAdressWhitelist")
		defer span.End()

		if len(whitelistedAddress) == 0 {
			logger.Info("address whitelisting not enabled. Skipping middleware")
			return nil
		}

		for _, userop := range ctx.Batch {

			err := db.View(func(txn *badger.Txn) error {
				_, err := txn.Get([]byte(getWhitelistKey(userop.Sender)))
				return err
			})

			if err != nil {
				logger.Error(err, "sender not found in whitelist. cannot execute transaction",
					"address", userop.Sender.Hex())
				return err
			}
		}

		return nil
	}
}
