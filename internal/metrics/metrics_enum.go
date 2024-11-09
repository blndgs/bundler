// Code generated by go-enum DO NOT EDIT.
// Version:
// Revision:
// Build Date:
// Built By:

package metrics

import (
	"errors"
	"fmt"
)

const (
	// UserOpCounterStatusSuccessful is a UserOpCounterStatus of type successful.
	UserOpCounterStatusSuccessful UserOpCounterStatus = "successful"
	// UserOpCounterStatusPending is a UserOpCounterStatus of type pending.
	UserOpCounterStatusPending UserOpCounterStatus = "pending"
	// UserOpCounterStatusFailed is a UserOpCounterStatus of type failed.
	UserOpCounterStatusFailed UserOpCounterStatus = "failed"
)

var ErrInvalidUserOpCounterStatus = errors.New("not a valid UserOpCounterStatus")

// String implements the Stringer interface.
func (x UserOpCounterStatus) String() string {
	return string(x)
}

// IsValid provides a quick way to determine if the typed value is
// part of the allowed enumerated values
func (x UserOpCounterStatus) IsValid() bool {
	_, err := ParseUserOpCounterStatus(string(x))
	return err == nil
}

var _UserOpCounterStatusValue = map[string]UserOpCounterStatus{
	"successful": UserOpCounterStatusSuccessful,
	"pending":    UserOpCounterStatusPending,
	"failed":     UserOpCounterStatusFailed,
}

// ParseUserOpCounterStatus attempts to convert a string to a UserOpCounterStatus.
func ParseUserOpCounterStatus(name string) (UserOpCounterStatus, error) {
	if x, ok := _UserOpCounterStatusValue[name]; ok {
		return x, nil
	}
	return UserOpCounterStatus(""), fmt.Errorf("%s is %w", name, ErrInvalidUserOpCounterStatus)
}
