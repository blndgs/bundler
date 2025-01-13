package srv

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/blndgs/bundler/store"
	"github.com/blndgs/bundler/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/stackup-wallet/stackup-bundler/pkg/modules"
)

// CheckSenderWhitelist is an handler that checks to limit on-chain userops to a set of
// addresses. These addresses must be initialized on start up and they must be a comma seperated list
// ERC4337_BUNDLER_ADDRESS_WHITELIST=0xAddress1,0xAddress2
// If not provided, this middleware will always act as a no-op
//
// It also returns a function that acts as a clean up operation
// We do not want to persist whitelisted addresses between restarts
// They must always come fresh from the env variable
func CheckSenderWhitelist(
	store *store.BadgerStore,
	whitelistedAddresses []common.Address,
	logger logr.Logger,
	txHashes *xsync.MapOf[string, OpHashes],
	entrypointAddr common.Address,
	chainID *big.Int) (modules.BatchHandlerFunc, func() error) {

	logger.Info("Setting up addresses in BadgerDB for whitelist checks")

	if len(whitelistedAddresses) > 0 {
		logger.Info("Bundler address whitelisting enabled", "number_of_whitelisted_addresses", len(whitelistedAddresses))
	}

	// Initialize whitelist with provided addresses
	// Use a single transaction to store all addresses
	if err := store.AddToWhitelist(context.Background(), whitelistedAddresses); err != nil {
		logger.Error(err, "Failed to store addresses in BadgerDB")
		return nil, func() error { return nil }
	}

	return func(ctx *modules.BatchHandlerCtx) error {
			_, span := utils.GetTracer().
				Start(context.Background(), "WithAddressWhitelist")
			defer span.End()

			if len(whitelistedAddresses) == 0 {
				logger.Info("Address whitelisting not enabled. Skipping handler")
				return nil
			}

			for idx, userop := range ctx.Batch {
				isWhitelisted, err := store.IsWhitelisted(context.Background(), userop.Sender)
				if err != nil {
					logger.Error(err, "Error checking whitelist status", "address", userop.Sender.Hex())
					return fmt.Errorf("whitelist check failed: %w", err)
				}

				if !isWhitelisted {
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
			// Cleanup function - remove all whitelisted addresses
			return store.RemoveFromWhitelist(context.Background(), whitelistedAddresses)
		}
}
