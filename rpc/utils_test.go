package rpc

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func Test_GetClientIPFromXFF(t *testing.T) {

	tt := []struct {
		name       string
		headerList []string
		expected   string
	}{
		{
			name:       "Heaer contains just one IP",
			headerList: []string{"192.168.0.1"},
			expected:   "192.168.0.1",
		},
		{
			name:       "Heaer contains just one IP",
			headerList: []string{"127.0.0.1", "192.168.0.1"},
			expected:   "127.0.0.1",
		},
	}

	for _, v := range tt {
		t.Run(v.name, func(t *testing.T) {

			req := httptest.NewRequest(http.MethodGet, "/", strings.NewReader("{}"))

			for _, header := range v.headerList {
				req.Header.Add("X-Forwarded-For", header)
			}

			ctx := &gin.Context{
				Request: req,
			}

			require.Equal(t, v.expected, GetClientIPFromXFF(ctx))
		})
	}
}

type MockSpan struct {
	attributes map[string]string
}

func (m *MockSpan) SetAttributes(attrs ...attribute.KeyValue) {
	if m.attributes == nil {
		m.attributes = make(map[string]string)
	}
	for _, attr := range attrs {
		m.attributes[string(attr.Key)] = attr.Value.AsString()
	}
}

func TestParseAndSetBlockNumber(t *testing.T) {
	tests := []struct {
		name           string
		params         []interface{}
		expectedNumber *big.Int
		expectedAttr   string
		expectError    bool
	}{
		{"No params", []interface{}{}, nil, "latest", false},
		{"Latest block", []interface{}{nil, "latest"}, nil, "latest", false},
		{"Pending block", []interface{}{nil, "pending"}, nil, "pending", false},
		{"Earliest block", []interface{}{nil, "earliest"}, nil, "earliest", false},
		{"Numeric block", []interface{}{nil, "12345"}, big.NewInt(12345), "12345", false},
		{"Hex block", []interface{}{nil, "0x1E240"}, big.NewInt(123456), "123456", false},
		{"Invalid block", []interface{}{nil, "invalid"}, nil, "", true},
		{"Non-string param", []interface{}{nil, 12345}, nil, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSpan := &MockSpan{}
			result, err := ParseAndSetBlockNumber(mockSpan, tt.params)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected an error, but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				if tt.expectedNumber == nil {
					if result != nil {
						t.Errorf("Expected nil, but got %v", result)
					}
				} else {
					if result == nil {
						t.Errorf("Expected %v, but got nil", tt.expectedNumber)
					} else if result.Cmp(tt.expectedNumber) != 0 {
						t.Errorf("Expected %v, but got %v", tt.expectedNumber, result)
					}
				}

				attr, exists := mockSpan.attributes["block_number"]
				if !exists {
					t.Errorf("Expected block_number attribute to be set")
				} else if attr != tt.expectedAttr {
					t.Errorf("Expected attribute %s, but got %s", tt.expectedAttr, attr)
				}
			}
		})
	}
}

func TestParseBlockNumber(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectedNum *big.Int
		expectError bool
	}{
		{"Latest block", "latest", nil, false},
		{"Pending block", "pending", nil, false},
		{"Earliest block", "earliest", nil, false},
		{"Decimal block number", "12345", big.NewInt(12345), false},
		{"Hex block number with prefix", "0x1e240", big.NewInt(123456), false},
		{"Hex block number without prefix", "1e240", big.NewInt(123456), false},
		{"Zero block number", "0", big.NewInt(0), false},
		{"Negative block number", "-1", nil, true},
		{"Invalid hex", "0xG1234", nil, true},
		{"Empty string", "", nil, true},
		{"Non-numeric string as hex", "abc", big.NewInt(2748), false},
		{"Very large hex number", "0xffffffffffffffff", new(big.Int).SetUint64(18446744073709551615), false},
		{"Very large decimal number", "18446744073709551615", new(big.Int).SetUint64(18446744073709551615), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseBlockNumber(tt.input)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected an error, but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				if tt.expectedNum == nil {
					if result != nil {
						t.Errorf("Expected nil, but got %v", result)
					}
				} else {
					if result == nil {
						t.Errorf("Expected %v, but got nil", tt.expectedNum)
					} else if result.Cmp(tt.expectedNum) != 0 {
						t.Errorf("Expected %v, but got %v", tt.expectedNum, result)
					}
				}
			}
		})
	}
}
