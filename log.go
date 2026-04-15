package pget

import "log/slog"

var logger = slog.Default()

// SetLogger allows applications/CLIs to inject a configured slog.Logger.
func SetLogger(l *slog.Logger) {
	if l != nil {
		logger = l
	}
}

