package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	pb "github.com/blndgs/model/gen/go/proto/v1"
	"github.com/dgraph-io/badger/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-logr/logr"
)

// Common errors returned by the database package.
var (
	ErrNotFound          = errors.New("item not found in database") // ErrNotFound is returned when a requested item does not exist in the database.
	ErrInvalidKey        = errors.New("invalid key provided")       // ErrInvalidKey is returned when an invalid or empty key is provided.
	ErrSerializationFail = errors.New("failed to serialize data")   // ErrSerializationFail is returned when data fails to serialize into JSON.
	ErrDeserializeFail   = errors.New("failed to deserialize data") // ErrDeserializeFail is returned when data fails to deserialize from JSON.
)

// Database key prefixes.
const (
	whitelistPrefix = "whitelist"
	statusPrefix    = "status"
)

// Status represents the processing status of a UserOperation, including the original user operation hash,
// the solved (or finalized) hash, and the current status.
type Status struct {
	OriginalHash string              `json:"originalHash"` // OriginalHash is the hash of the original UserOperation.
	SolvedHash   string              `json:"solvedHash"`   // SolvedHash is the hash of the UserOperation after processing is completed.
	Status       pb.ProcessingStatus `json:"status"`       // Status represents the current processing status of the UserOperation.
}

// WhitelistEntry represents a whitelisted address along with its associated metadata.
type WhitelistEntry struct {
	Address common.Address `json:"address"` // Address is the Ethereum address that is whitelisted.
}

// BadgerStore implements Store interface using BadgerDB.
type BadgerStore struct {
	db     *badger.DB
	logger logr.Logger
}

// NewBadgerStore creates a new BadgerDB-backed store with a logger
func NewBadgerStore(db *badger.DB, logger logr.Logger) *BadgerStore {
	return &BadgerStore{
		db:     db,
		logger: logger,
	}
}

// GetStatus retrieves the Status associated with a given UserOperation hash.
// If the hash is empty or does not exist in the database, it returns an error.
//
// Params:
//   - ctx: Context for request cancellation and timeouts.
//   - hash: The hash key of the UserOperation.
//
// Returns:
//   - *Status: The retrieved status entry.
//   - error: Non-nil if the retrieval fails due to invalid key, not found item, or other errors.
func (s *BadgerStore) GetStatus(
	ctx context.Context,
	hash string) (*Status, error) {
	if hash == "" {
		s.logger.Error(ErrInvalidKey, "empty hash provided")
		return nil, fmt.Errorf("%w: empty hash", ErrInvalidKey)
	}

	var status Status
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(formatStatusKey(hash)))
		if err == badger.ErrKeyNotFound {
			s.logger.Info("status not found", "hash", hash)
			return ErrNotFound
		}
		if err != nil {
			s.logger.Error(err, "failed to get status from db", "hash", hash)
			return fmt.Errorf("badger get failed: %w", err)
		}

		return item.Value(func(val []byte) error {
			if err := json.Unmarshal(val, &status); err != nil {
				s.logger.Error(err, "failed to unmarshal status", "hash", hash)
				return fmt.Errorf("%w: %v", ErrDeserializeFail, err)
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info("retrieved status", "hash", hash, "status", status.Status)
	return &status, nil
}

// UpdateStatus updates or creates a new Status entry for a given UserOperation.
//
// It stores the status under both the current hash and the solved hash keys
// if they differ. This allows retrieval by either hash.
//
// Params:
//   - ctx: Context for request cancellation and timeouts.
//   - currentHash: The original or current hash of the UserOperation.
//   - solvedHash: The solved (final) hash of the UserOperation.
//   - status: The processing status to be stored.
//
// Returns:
//   - error: Non-nil if the update fails due to invalid keys, serialization issues, or transaction errors.

func (s *BadgerStore) UpdateStatus(
	ctx context.Context,
	currentHash,
	solvedHash string,
	status pb.ProcessingStatus) error {
	if currentHash == "" || solvedHash == "" {
		s.logger.Error(ErrInvalidKey, "empty hash provided", "currentHash", currentHash, "solvedHash", solvedHash)
		return fmt.Errorf("%w: empty hash provided", ErrInvalidKey)
	}

	statusData := Status{
		OriginalHash: currentHash,
		SolvedHash:   solvedHash,
		Status:       status,
	}

	value, err := json.Marshal(statusData)
	if err != nil {
		s.logger.Error(err, "failed to marshal status", "currentHash", currentHash, "solvedHash", solvedHash)
		return fmt.Errorf("%w: %v", ErrSerializationFail, err)
	}

	err = s.db.Update(func(txn *badger.Txn) error {
		// Store under solved hash
		if err := txn.Set([]byte(formatStatusKey(solvedHash)), value); err != nil {
			s.logger.Error(err, "failed to set solved status", "solvedHash", solvedHash)
			return fmt.Errorf("failed to set solved status: %w", err)
		}

		// Store under current hash if different
		if currentHash != solvedHash {
			if err := txn.Set([]byte(formatStatusKey(currentHash)), value); err != nil {
				s.logger.Error(err, "failed to set current status", "currentHash", currentHash)
				return fmt.Errorf("failed to set current status: %w", err)
			}
		}

		return nil
	})

	if err == nil {
		s.logger.Info("updated status", "currentHash", currentHash, "solvedHash", solvedHash, "status", status)
	}
	return err
}

// DeleteStatus removes the Status entry associated with the given hash.
//
// Params:
//   - ctx: Context for request cancellation and timeouts.
//   - hash: The hash of the UserOperation to delete.
//
// Returns:
//   - error: Non-nil if the deletion fails due to invalid key or database transaction errors.

func (s *BadgerStore) DeleteStatus(
	ctx context.Context,
	hash string) error {
	if hash == "" {
		s.logger.Error(ErrInvalidKey, "empty hash provided")
		return fmt.Errorf("%w: empty hash", ErrInvalidKey)
	}

	err := s.db.Update(func(txn *badger.Txn) error {
		err := txn.Delete([]byte(formatStatusKey(hash)))
		if err != nil {
			s.logger.Error(err, "failed to delete status", "hash", hash)
			return fmt.Errorf("failed to delete status: %w", err)
		}
		return nil
	})
	if err == nil {
		s.logger.Info("deleted status", "hash", hash)
	}
	return err
}

// IsWhitelisted checks if the given Ethereum address is present in the whitelist.
//
// Params:
//   - ctx: Context for request cancellation and timeouts.
//   - address: The Ethereum address to check.
//
// Returns:
//   - bool: True if the address is whitelisted, false otherwise.
//   - error: Non-nil if a database transaction error occurs.
func (s *BadgerStore) IsWhitelisted(
	ctx context.Context,
	address common.Address) (bool, error) {
	err := s.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte(formatWhitelistKey(address)))
		return err
	})

	if err == badger.ErrKeyNotFound {
		s.logger.Info("address not found in whitelist", "address", address.Hex())
		return false, nil
	}
	if err != nil {
		s.logger.Error(err, "whitelist check failed", "address", address.Hex())
		return false, fmt.Errorf("whitelist check failed: %w", err)
	}
	s.logger.Info("address found in whitelist", "address", address.Hex())
	return true, nil
}

// AddToWhitelist inserts the provided list of addresses into the whitelist.
//
// Params:
//   - ctx: Context for request cancellation and timeouts.
//   - addresses: A slice of Ethereum addresses to be added to the whitelist.
//
// Returns:
//   - error: Non-nil if serialization or database transaction errors occur.
func (s *BadgerStore) AddToWhitelist(
	ctx context.Context,
	addresses []common.Address) error {
	if len(addresses) == 0 {
		s.logger.Info("no addresses to add to whitelist")
		return nil
	}

	err := s.db.Update(func(txn *badger.Txn) error {
		for _, addr := range addresses {
			entry := WhitelistEntry{Address: addr}
			value, err := json.Marshal(entry)
			if err != nil {
				s.logger.Error(err, "failed to marshal whitelist entry", "address", addr.Hex())
				return fmt.Errorf("%w: %v", ErrSerializationFail, err)
			}

			if err := txn.Set([]byte(formatWhitelistKey(addr)), value); err != nil {
				s.logger.Error(err, "failed to add address to whitelist", "address", addr.Hex())
				return fmt.Errorf("failed to add address to whitelist: %w", err)
			}
			s.logger.Info("added address to whitelist", "address", addr.Hex())
		}
		return nil
	})
	if err == nil {
		s.logger.Info("completed adding addresses to whitelist", "count", len(addresses))
	}
	return err
}

// RemoveFromWhitelist deletes the given addresses from the whitelist.
//
// Params:
//   - ctx: Context for request cancellation and timeouts.
//   - addresses: A slice of Ethereum addresses to remove from the whitelist.
//
// Returns:
//   - error: Non-nil if database transaction errors occur.
func (s *BadgerStore) RemoveFromWhitelist(ctx context.Context, addresses []common.Address) error {
	if len(addresses) == 0 {
		s.logger.Info("no addresses to remove from whitelist")
		return nil
	}

	err := s.db.Update(func(txn *badger.Txn) error {
		for _, addr := range addresses {
			if err := txn.Delete([]byte(formatWhitelistKey(addr))); err != nil {
				s.logger.Error(err, "failed to remove address from whitelist", "address", addr.Hex())
				return fmt.Errorf("failed to remove address from whitelist: %w", err)
			}
			s.logger.Info("removed address from whitelist", "address", addr.Hex())
		}
		return nil
	})
	if err == nil {
		s.logger.Info("completed removing addresses from whitelist", "count", len(addresses))
	}
	return err
}

// GetWhitelistedAddresses retrieves all Ethereum addresses currently in the whitelist.
//
// Params:
//   - ctx: Context for request cancellation and timeouts.
//
// Returns:
//   - []common.Address: A slice of all whitelisted addresses.
//   - error: Non-nil if deserialization or database transaction errors occur.
func (s *BadgerStore) GetWhitelistedAddresses(ctx context.Context) ([]common.Address, error) {
	var addresses []common.Address

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(whitelistPrefix)

		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var entry WhitelistEntry
			err := item.Value(func(val []byte) error {
				return json.Unmarshal(val, &entry)
			})
			if err != nil {
				s.logger.Error(err, "failed to unmarshal whitelist entry")
				return fmt.Errorf("%w: %v", ErrDeserializeFail, err)
			}
			addresses = append(addresses, entry.Address)
		}
		return nil
	})

	if err == nil {
		s.logger.Info("retrieved whitelisted addresses", "count", len(addresses))
	}
	return addresses, err
}

// formatStatusKey constructs a key for storing and retrieving status data.
// The key format is: "status-<hash>"
func formatStatusKey(hash string) string {
	return fmt.Sprintf("%s-%s", statusPrefix, hash)
}

// formatWhitelistKey constructs a key for storing and retrieving whitelist entries.
// The key format is: "whitelist-<address>"
func formatWhitelistKey(address common.Address) string {
	return fmt.Sprintf("%s-%s", whitelistPrefix, address.Hex())
}
