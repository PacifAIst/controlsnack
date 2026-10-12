// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// newLogger builds the stderr logger. format is "text" (console encoder) or "json".
func newLogger(w io.Writer, level, format string) (*zap.Logger, error) {
	var lvl zapcore.Level
	switch level {
	case "debug":
		lvl = zapcore.DebugLevel
	case "info":
		lvl = zapcore.InfoLevel
	case "warn":
		lvl = zapcore.WarnLevel
	case "error":
		lvl = zapcore.ErrorLevel
	default:
		return nil, fmt.Errorf("unknown log level %q", level)
	}

	enc := zap.NewProductionEncoderConfig()
	enc.TimeKey = "time"
	enc.EncodeTime = zapcore.ISO8601TimeEncoder

	var encoder zapcore.Encoder
	switch format {
	case "text":
		enc.EncodeLevel = zapcore.CapitalLevelEncoder
		encoder = zapcore.NewConsoleEncoder(enc)
	case "json":
		encoder = zapcore.NewJSONEncoder(enc)
	default:
		return nil, fmt.Errorf("unknown log format %q", format)
	}
	return zap.New(zapcore.NewCore(encoder, zapcore.AddSync(w), lvl)), nil
}
