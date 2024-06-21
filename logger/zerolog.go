package logger

import (
	"os"

	"github.com/go-logr/logr"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
)

// NewZeroLogr returns a Zerolog logger wrapped in a go-logr/logr interface.
func NewZeroLogr(isDebugMode bool) logr.Logger {

	var level = zerolog.ErrorLevel
	if isDebugMode {
		level = zerolog.InfoLevel
	}

	zl := zerolog.New(os.Stderr).
		With().
		Caller().
		Timestamp().
		Logger().
		Level(level)

	return zerologr.New(&zl)
}
