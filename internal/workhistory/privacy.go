package workhistory

import (
	"regexp"
	"strings"

	"syncgate/internal/project"
)

var (
	secretPattern      = regexp.MustCompile(`(?i)(authorization|api[_ -]?key|token|password|secret)\s*[:=]\s*[^\s,;]+`)
	windowsPathPattern = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\)[^\r\n,;]+`)
)

func redactText(value string) string {
	value = secretPattern.ReplaceAllString(value, "$1=[redacted]")
	return windowsPathPattern.ReplaceAllString(value, "[redacted-path]")
}

func redactList(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = redactText(value)
	}
	return result
}

func sanitizeProducer(value project.Producer) project.Producer {
	value.Trade = redactText(value.Trade)
	value.Specialization = redactText(value.Specialization)
	value.Provider = redactText(value.Provider)
	value.Model = redactText(value.Model)
	value.ModelVersion = redactText(value.ModelVersion)
	return value
}

func validateMetadata(value Metadata) error {
	if strings.TrimSpace(value.RootPath) == "" || strings.TrimSpace(value.IdempotencyKey) == "" || len(value.IdempotencyKey) > 256 {
		return ErrInvalidRequest
	}
	if value.OccurredAt.IsZero() {
		return ErrInvalidRequest
	}
	_, offset := value.OccurredAt.Zone()
	if offset != 0 {
		return ErrInvalidRequest
	}
	return nil
}
