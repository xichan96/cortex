package providers

import (
	"log/slog"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

func EncodeMessageToolExtras(m types.Message) string {
	s, err := types.MarshalMessageToolPersist(m)
	if err != nil {
		logger.LogError("EncodeMessageToolExtras", err, slog.String("role", m.Role))
		return ""
	}
	return s
}

func ApplyStoredToolExtras(m *types.Message, s string) {
	if err := types.ApplyMessageToolPersist(m, s); err != nil {
		logger.LogError("ApplyStoredToolExtras", err, slog.String("role", m.Role))
	}
}
