package logx

import (
	"os"
	"strings"

	"github.com/charmbracelet/log"
)

var debug *log.Logger

func init() {
	if strings.TrimSpace(os.Getenv("GIT_GREEN_DEBUG")) == "" {
		return
	}
	debug = log.NewWithOptions(os.Stderr, log.Options{
		Level:           log.DebugLevel,
		ReportTimestamp: true,
		Prefix:          "git-green",
	})
}

// Debug emits a debug log line when GIT_GREEN_DEBUG is non-empty.
func Debug(msg string, kv ...any) {
	if debug != nil {
		debug.Debug(msg, kv...)
	}
}
