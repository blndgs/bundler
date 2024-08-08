package rpc

import (
	"fmt"
	"math/big"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
)

// GetClientIPFromXFF returns the client ID using x-forwarded-for headers before relying on c.ClientIP().
// This assumes use of a trusted proxy.
func GetClientIPFromXFF(c *gin.Context) string {
	forwardHeader := c.Request.Header.Get("x-forwarded-for")
	firstAddress := strings.Split(forwardHeader, ",")[0]
	if net.ParseIP(strings.TrimSpace(firstAddress)) != nil {
		return firstAddress
	}

	return c.ClientIP()
}

type SpanSetAttr interface {
	SetAttributes(...attribute.KeyValue)
}

// ParseAndSetBlockNumber parses the block number from the provided parameters and sets it as an attribute on the given span.
//
// Parameters:
//   - span: A SpanSetAttr interface for setting attributes.
//   - params: A slice of interface{} containing the RPC method parameters. The block number is expected to be the second parameter (index 1) if present.
//
// Returns:
//   - *big.Int: The parsed block number as a big.Int. Returns nil for "latest", "pending", or "earliest".
//   - error: An error if the block number parsing fails or if the parameter is invalid.
//
// The function handles the following cases:
//   - No block number provided: Sets the attribute to "latest".
//   - Special strings "latest", "pending", "earliest": Sets the attribute accordingly.
//   - Numeric strings (decimal or hex): Parses and sets the numeric value.
//
// If successful, it sets the "block_number" attribute on the provided span with the parsed or default value.
func ParseAndSetBlockNumber(span SpanSetAttr, params []interface{}) (*big.Int, error) {
	var blockNumber *big.Int
	var blockParam string

	if len(params) > 1 {
		var ok bool
		blockParam, ok = params[1].(string)
		if !ok {
			return nil, fmt.Errorf("invalid block parameter type")
		}

		var err error
		blockNumber, err = ParseBlockNumber(blockParam)
		if err != nil {
			return nil, fmt.Errorf("invalid block number: %w", err)
		}
	}

	if blockNumber != nil {
		span.SetAttributes(attribute.String("block_number", blockNumber.String()))
	} else {
		if blockParam == "" {
			blockParam = "latest"
		}
		span.SetAttributes(attribute.String("block_number", blockParam))
	}

	return blockNumber, nil
}

// ParseBlockNumber converts a string representation of a block number to a *big.Int.
//
// Parameters:
//   - blockParam: A string representing the block number. It Can be a numeric string
//     (decimal or hexadecimal with "0x" prefix),
//     or one of the special values "latest", "pending", or "earliest".
//
// Returns:
//   - *big.Int: The parsed block number as a big.Int. Returns nil for "latest", "pending", or "earliest".
//   - error: An error if the parsing fails or if the input is invalid.
//
// The function handles the following cases:
//   - "latest", "pending", "earliest": Returns (nil, nil).
//   - Hexadecimal (with "0x" prefix): Parses as hexadecimal.
//   - Decimal: Attempts to parse as decimal first, then as hexadecimal if decimal parsing fails.
//
// Note: The function returns an error for negative numbers or invalid string formats.
func ParseBlockNumber(blockParam string) (*big.Int, error) {
	if blockParam == "latest" || blockParam == "pending" || blockParam == "earliest" {
		return nil, nil
	}

	var num *big.Int
	var success bool

	if len(blockParam) > 2 && blockParam[:2] == "0x" {
		// Hexadecimal with prefix
		num, success = new(big.Int).SetString(blockParam[2:], 16)
	} else {
		// Try parsing as decimal first
		num, success = new(big.Int).SetString(blockParam, 10)
		if !success {
			// If decimal parsing fails, try as hexadecimal without prefix
			num, success = new(big.Int).SetString(blockParam, 16)
		}
	}

	if !success {
		return nil, fmt.Errorf("invalid block number: %s", blockParam)
	}

	if num.Sign() < 0 {
		return nil, fmt.Errorf("block number must be non-negative: %s", blockParam)
	}

	return num, nil
}
