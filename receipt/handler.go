package receipt

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/blndgs/bundler/store"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint"
	"github.com/stackup-wallet/stackup-bundler/pkg/entrypoint/filter"
)

// UserOperationReceipt represents the receipt of a UserOperation along with accompanying transaction details.
type UserOperationReceipt struct {
	UserOpHash    common.Hash        `json:"userOpHash"`
	Sender        common.Address     `json:"sender"`
	Paymaster     common.Address     `json:"paymaster"`
	Nonce         string             `json:"nonce"`
	Success       bool               `json:"success"`
	ActualGasCost string             `json:"actualGasCost"`
	ActualGasUsed string             `json:"actualGasUsed"`
	From          common.Address     `json:"from"`
	Receipt       *parsedTransaction `json:"receipt"`
	Logs          []*types.Log       `json:"logs"`
	Reason        string             `json:"reason,omitempty"`
}

// parsedTransaction represents the parsed details of an Ethereum transaction.
type parsedTransaction struct {
	BlockHash         common.Hash    `json:"blockHash"`
	BlockNumber       string         `json:"blockNumber"`
	From              common.Address `json:"from"`
	CumulativeGasUsed string         `json:"cumulativeGasUsed"`
	GasUsed           string         `json:"gasUsed"`
	Logs              []*types.Log   `json:"logs"`
	LogsBloom         types.Bloom    `json:"logsBloom"`
	TransactionHash   common.Hash    `json:"transactionHash"`
	TransactionIndex  string         `json:"transactionIndex"`
	EffectiveGasPrice string         `json:"effectiveGasPrice"`
}

// GetUserOpReceiptFunc is a general interface for fetching a UserOperationReceipt given a userOpHash, EntryPoint address, and block range.
type GetUserOpReceiptFunc = func(hash string, ep common.Address, blkRange uint64) (*UserOperationReceipt, error)

// GetUserOpReceiptWithEthClient returns an implementation of GetUserOpReceiptFunc that relies on an eth client to fetch a UserOperationReceipt.
func GetUserOpReceiptWithEthClient(eth *ethclient.Client, store *store.BadgerStore) GetUserOpReceiptFunc {
	return func(hash string, ep common.Address, blkRange uint64) (*UserOperationReceipt, error) {
		return GetUserOperationReceipt(eth, store, hash, ep, blkRange)
	}
}

// GetUserOperationReceipt retrieves the receipt for a specific UserOperation based on its hash.
func GetUserOperationReceipt(
	eth *ethclient.Client,
	store *store.BadgerStore,
	userOpHash string,
	entryPoint common.Address,
	blkRange uint64,
) (*UserOperationReceipt, error) {
	if !filter.IsValidUserOpHash(userOpHash) {
		return nil, errors.New("missing/invalid userOpHash")
	}
	ctx := context.Background()

	// Retrieve status from DB
	status, err := store.GetStatus(ctx, userOpHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %w", err)
	}

	// Try to retrieve the event and process it
	receipt, err := processEvent(eth, userOpHash, entryPoint, blkRange, status)
	if receipt != nil || err != nil {
		return receipt, err
	}

	// If not found, try with solved hash from status
	if status.SolvedHash != "" && status.SolvedHash != userOpHash {
		receipt, err = processEvent(eth, status.SolvedHash, entryPoint, blkRange, status)
		if receipt != nil || err != nil {
			return receipt, err
		}
	}

	// If no events found, return a receipt with just the status
	return &UserOperationReceipt{
		Reason:     status.Status.String(),
		UserOpHash: common.HexToHash(userOpHash),
	}, nil
}

// processEvent processes the UserOperationEvent and constructs the receipt.
func processEvent(
	eth *ethclient.Client,
	hash string,
	entryPoint common.Address,
	blkRange uint64,
	status *store.Status,
) (*UserOperationReceipt, error) {
	it, err := filterUserOperationEvent(eth, hash, entryPoint, blkRange)
	if err != nil {
		return nil, err
	}

	if it.Next() {
		tx, isPending, err := eth.TransactionByHash(context.Background(), it.Event.Raw.TxHash)
		if err != nil {
			return nil, err
		} else if isPending {
			return nil, nil
		}

		from, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
		if err != nil {
			return nil, err
		}

		receipt, err := eth.TransactionReceipt(context.Background(), it.Event.Raw.TxHash)
		if err != nil {
			return nil, err
		}

		txnReceipt := &parsedTransaction{
			BlockHash:         receipt.BlockHash,
			BlockNumber:       hexutil.EncodeBig(receipt.BlockNumber),
			From:              from,
			CumulativeGasUsed: hexutil.EncodeBig(big.NewInt(0).SetUint64(receipt.CumulativeGasUsed)),
			GasUsed:           hexutil.EncodeBig(big.NewInt(0).SetUint64(receipt.GasUsed)),
			Logs:              receipt.Logs,
			LogsBloom:         receipt.Bloom,
			TransactionHash:   receipt.TxHash,
			TransactionIndex:  hexutil.EncodeBig(big.NewInt(0).SetUint64(uint64(receipt.TransactionIndex))),
			EffectiveGasPrice: hexutil.EncodeBig(tx.GasPrice()),
		}

		return &UserOperationReceipt{
			Reason:        status.Status.String(),
			UserOpHash:    it.Event.UserOpHash,
			Sender:        it.Event.Sender,
			Paymaster:     it.Event.Paymaster,
			Nonce:         hexutil.EncodeBig(it.Event.Nonce),
			Success:       it.Event.Success,
			ActualGasCost: hexutil.EncodeBig(it.Event.ActualGasCost),
			ActualGasUsed: hexutil.EncodeBig(it.Event.ActualGasUsed),
			From:          from,
			Receipt:       txnReceipt,
			Logs:          []*types.Log{&it.Event.Raw},
		}, nil
	}
	return nil, nil
}

// filterUserOperationEvent filters UserOperationEvents emitted by the EntryPoint contract within a specified block range.
func filterUserOperationEvent(
	eth *ethclient.Client,
	userOpHash string,
	entryPoint common.Address,
	blkRange uint64,
) (*entrypoint.EntrypointUserOperationEventIterator, error) {
	// Create a new EntryPoint instance
	ep, err := entrypoint.NewEntrypoint(entryPoint, eth)
	if err != nil {
		return nil, err
	}

	// Retrieve the latest block number
	bn, err := eth.BlockNumber(context.Background())
	if err != nil {
		return nil, err
	}

	// Calculate the block range for filtering
	toBlk := big.NewInt(0).SetUint64(bn)
	startBlk := big.NewInt(0)
	if subBlkRange := big.NewInt(0).Sub(toBlk, big.NewInt(0).SetUint64(blkRange)); subBlkRange.Cmp(startBlk) > 0 {
		startBlk = subBlkRange
	}

	// Filter UserOperationEvents within the block range
	return ep.FilterUserOperationEvent(
		&bind.FilterOpts{Start: startBlk.Uint64()},
		[][32]byte{common.HexToHash(userOpHash)},
		[]common.Address{},
		[]common.Address{},
	)
}
