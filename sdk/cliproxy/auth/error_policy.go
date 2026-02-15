package auth

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrorKind is a normalized machine-readable failure category used by
// selector cooling/disable policy and management API observability.
type ErrorKind string

const (
	ErrorKindQuotaLimited5h       ErrorKind = "quota_limited_5h"
	ErrorKindQuotaLimited7d       ErrorKind = "quota_limited_7d"
	ErrorKindQuotaLimited         ErrorKind = "quota_limited"
	ErrorKindUnauthorized         ErrorKind = "unauthorized"
	ErrorKindWorkspaceDeactivated ErrorKind = "workspace_deactivated"
	ErrorKindForbidden            ErrorKind = "forbidden"
	ErrorKindTransientUpstream    ErrorKind = "transient_upstream"
	ErrorKindNetworkError         ErrorKind = "network_error"
	ErrorKindAccountDeactivated   ErrorKind = "account_deactivated"
	ErrorKindUnknown              ErrorKind = "unknown"
)

func classifyResultError(resultErr *Error, retryAfter *time.Duration) (ErrorKind, string, *time.Duration, bool) {
	if resultErr == nil {
		return ErrorKindUnknown, "", nil, false
	}

	status := resultErr.HTTPStatus
	message := strings.TrimSpace(resultErr.Message)
	reason := extractErrorReason(message)
	lowerReason := strings.ToLower(reason)
	lowerMessage := strings.ToLower(message)

	if kind := normalizeErrorKind(resultErr.Code); kind != ErrorKindUnknown {
		return kind, reason, retryAfter, isFatalErrorKind(kind)
	}

	if status == 0 && looksLikeNetworkError(lowerMessage) {
		delay := 15 * time.Second
		return ErrorKindNetworkError, reason, &delay, false
	}

	switch status {
	case 429:
		effectiveRetryAfter := retryAfter
		if effectiveRetryAfter == nil {
			effectiveRetryAfter = parseRetryAfterHintFromMessage(message)
		}
		kind := classifyQuotaKind(effectiveRetryAfter, lowerMessage)
		return kind, reason, effectiveRetryAfter, false
	case 401:
		if isAccountDeactivatedReason(lowerReason) {
			return ErrorKindAccountDeactivated, reason, nil, true
		}
		return ErrorKindUnauthorized, reason, nil, false
	case 402:
		return ErrorKindWorkspaceDeactivated, reason, nil, true
	case 403:
		if isAccountDeactivatedReason(lowerReason) {
			return ErrorKindAccountDeactivated, reason, nil, true
		}
		return ErrorKindForbidden, reason, nil, false
	case 408, 500, 502, 503, 504:
		return ErrorKindTransientUpstream, reason, nil, false
	default:
		if looksLikeNetworkError(lowerMessage) {
			delay := 15 * time.Second
			return ErrorKindNetworkError, reason, &delay, false
		}
		return ErrorKindUnknown, reason, retryAfter, false
	}
}

func normalizeErrorKind(raw string) ErrorKind {
	switch ErrorKind(strings.TrimSpace(strings.ToLower(raw))) {
	case ErrorKindQuotaLimited5h,
		ErrorKindQuotaLimited7d,
		ErrorKindQuotaLimited,
		ErrorKindUnauthorized,
		ErrorKindWorkspaceDeactivated,
		ErrorKindForbidden,
		ErrorKindTransientUpstream,
		ErrorKindNetworkError,
		ErrorKindAccountDeactivated:
		return ErrorKind(strings.TrimSpace(strings.ToLower(raw)))
	default:
		return ErrorKindUnknown
	}
}

func isFatalErrorKind(kind ErrorKind) bool {
	switch kind {
	case ErrorKindAccountDeactivated, ErrorKindWorkspaceDeactivated:
		return true
	default:
		return false
	}
}

func parseRetryAfterHintFromMessage(message string) *time.Duration {
	if fromJSON := parseRetryAfterHintFromJSON(message); fromJSON != nil {
		return fromJSON
	}

	msg := strings.ToLower(message)
	if msg == "" {
		return nil
	}

	if strings.Contains(msg, "7d") || strings.Contains(msg, "7 day") || strings.Contains(msg, "7-day") {
		delay := 7 * 24 * time.Hour
		return &delay
	}
	if strings.Contains(msg, "5h") || strings.Contains(msg, "5 hour") || strings.Contains(msg, "5-hour") {
		delay := 5 * time.Hour
		return &delay
	}

	durationPattern := regexp.MustCompile(`(\d+)\s*(d|h|m|s)\b`)
	matches := durationPattern.FindAllStringSubmatch(msg, -1)
	if len(matches) > 0 {
		var total time.Duration
		for _, match := range matches {
			value, err := strconv.Atoi(match[1])
			if err != nil || value <= 0 {
				continue
			}
			switch match[2] {
			case "d":
				total += time.Duration(value) * 24 * time.Hour
			case "h":
				total += time.Duration(value) * time.Hour
			case "m":
				total += time.Duration(value) * time.Minute
			case "s":
				total += time.Duration(value) * time.Second
			}
		}
		if total > 0 {
			return &total
		}
	}

	secondsPattern := regexp.MustCompile(`after\s+(\d+)\s*seconds?`)
	if matches := secondsPattern.FindStringSubmatch(msg); len(matches) > 1 {
		seconds, err := strconv.Atoi(matches[1])
		if err == nil && seconds > 0 {
			delay := time.Duration(seconds) * time.Second
			return &delay
		}
	}

	return nil
}

func parseRetryAfterHintFromJSON(message string) *time.Duration {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil
	}

	now := time.Now()
	if d := parseRetryAfterHintFromNode(payload, now); d != nil {
		return d
	}
	if errorNode, ok := payload["error"].(map[string]any); ok {
		if d := parseRetryAfterHintFromNode(errorNode, now); d != nil {
			return d
		}
	}
	if detailNode, ok := payload["detail"].(map[string]any); ok {
		if d := parseRetryAfterHintFromNode(detailNode, now); d != nil {
			return d
		}
	}

	return nil
}

func parseRetryAfterHintFromNode(node map[string]any, now time.Time) *time.Duration {
	if node == nil {
		return nil
	}

	// Common provider payloads:
	// - {"error":{"resets_in_seconds":260750}}
	// - {"error":{"resets_at":1771331564}}
	// - {"detail":{"retry_after_seconds":300}}
	secondsKeys := []string{
		"resets_in_seconds",
		"retry_after_seconds",
		"retry_in_seconds",
		"reset_in_seconds",
	}
	for _, key := range secondsKeys {
		if raw, ok := node[key]; ok {
			if seconds, ok := numericValue(raw); ok && seconds > 0 {
				delay := time.Duration(seconds * float64(time.Second))
				return &delay
			}
		}
	}

	timestampKeys := []string{
		"resets_at",
		"reset_at",
		"retry_after_at",
		"retry_at",
	}
	for _, key := range timestampKeys {
		if raw, ok := node[key]; ok {
			if ts, ok := numericValue(raw); ok && ts > 0 {
				recoverAt := time.Unix(int64(ts), 0)
				delay := time.Until(recoverAt)
				if delay > 0 {
					return &delay
				}
			}
		}
	}

	if raw, ok := node["retry_after"]; ok {
		if seconds, ok := numericValue(raw); ok && seconds > 0 {
			delay := time.Duration(seconds * float64(time.Second))
			return &delay
		}
	}

	if nested, ok := node["error"].(map[string]any); ok {
		if d := parseRetryAfterHintFromNode(nested, now); d != nil {
			return d
		}
	}
	if nested, ok := node["detail"].(map[string]any); ok {
		if d := parseRetryAfterHintFromNode(nested, now); d != nil {
			return d
		}
	}

	return nil
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func classifyQuotaKind(retryAfter *time.Duration, lowerMessage string) ErrorKind {
	if retryAfter != nil {
		if *retryAfter >= 7*24*time.Hour-15*time.Minute {
			return ErrorKindQuotaLimited7d
		}
		if *retryAfter >= 5*time.Hour-5*time.Minute {
			return ErrorKindQuotaLimited5h
		}
	}
	if strings.Contains(lowerMessage, "7d") || strings.Contains(lowerMessage, "7 day") || strings.Contains(lowerMessage, "weekly") {
		return ErrorKindQuotaLimited7d
	}
	if strings.Contains(lowerMessage, "5h") || strings.Contains(lowerMessage, "5 hour") || strings.Contains(lowerMessage, "session") {
		return ErrorKindQuotaLimited5h
	}
	return ErrorKindQuotaLimited
}

func looksLikeNetworkError(lowerMessage string) bool {
	if lowerMessage == "" {
		return false
	}
	networkHints := []string{
		"connection refused",
		"connection reset",
		"dial tcp",
		"timeout",
		"tls",
		"no such host",
		"temporary failure",
		"eof",
		"network is unreachable",
		"context deadline exceeded",
		"proxyconnect",
	}
	for _, hint := range networkHints {
		if strings.Contains(lowerMessage, hint) {
			return true
		}
	}
	return false
}

func isAccountDeactivatedReason(lowerReason string) bool {
	if lowerReason == "" {
		return false
	}
	fatalHints := []string{
		"account deactivated",
		"workspace deactivated",
		"account banned",
		"account suspended",
		"subscription inactive",
		"token revoked",
		"token invalidated",
		"token_invalidated",
		"authentication token has been invalidated",
		"user disabled",
	}
	for _, hint := range fatalHints {
		if strings.Contains(lowerReason, hint) {
			return true
		}
	}
	return false
}

func extractErrorReason(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return trimmed
	}
	if errNode, ok := payload["error"].(map[string]any); ok {
		if msg, ok := errNode["message"].(string); ok && strings.TrimSpace(msg) != "" {
			return strings.TrimSpace(msg)
		}
		if code, ok := errNode["code"].(string); ok && strings.TrimSpace(code) != "" {
			return strings.TrimSpace(code)
		}
	}
	if msg, ok := payload["message"].(string); ok && strings.TrimSpace(msg) != "" {
		return strings.TrimSpace(msg)
	}
	return trimmed
}
