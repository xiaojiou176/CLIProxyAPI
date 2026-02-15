package auth

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const runtimeStateMetadataKey = "_runtime_state"

func syncRuntimeStateToMetadata(auth *Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}

	lastErrKind := ""
	lastErrReason := ""
	lastErrCode := 0
	lastErrRetryable := false
	lastErrAt := auth.UpdatedAt
	if auth.LastError != nil {
		lastErrKind = strings.TrimSpace(auth.LastError.Code)
		lastErrReason = strings.TrimSpace(auth.LastError.Message)
		lastErrCode = auth.LastError.HTTPStatus
		lastErrRetryable = auth.LastError.Retryable
	}

	cooldownUntil := auth.NextRetryAfter
	if auth.Quota.NextRecoverAt.After(cooldownUntil) {
		cooldownUntil = auth.Quota.NextRecoverAt
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.NextRetryAfter.After(cooldownUntil) {
			cooldownUntil = state.NextRetryAfter
		}
		if state.Quota.NextRecoverAt.After(cooldownUntil) {
			cooldownUntil = state.Quota.NextRecoverAt
		}
	}

	modelStates := map[string]any{}
	for model, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		modelStates[model] = map[string]any{
			"status":          string(state.Status),
			"status_message":  strings.TrimSpace(state.StatusMessage),
			"unavailable":     state.Unavailable,
			"next_retry_after": formatRuntimeTime(state.NextRetryAfter),
			"updated_at":      formatRuntimeTime(state.UpdatedAt),
			"quota": map[string]any{
				"exceeded":        state.Quota.Exceeded,
				"reason":          strings.TrimSpace(state.Quota.Reason),
				"next_recover_at": formatRuntimeTime(state.Quota.NextRecoverAt),
				"backoff_level":   state.Quota.BackoffLevel,
			},
			"last_error": encodeRuntimeError(state.LastError),
		}
	}

	auth.Metadata[runtimeStateMetadataKey] = map[string]any{
		"status":           string(auth.Status),
		"status_message":   strings.TrimSpace(auth.StatusMessage),
		"unavailable":      auth.Unavailable,
		"next_retry_after": formatRuntimeTime(auth.NextRetryAfter),
		"updated_at":       formatRuntimeTime(auth.UpdatedAt),
		"quota": map[string]any{
			"exceeded":        auth.Quota.Exceeded,
			"reason":          strings.TrimSpace(auth.Quota.Reason),
			"next_recover_at": formatRuntimeTime(auth.Quota.NextRecoverAt),
			"backoff_level":   auth.Quota.BackoffLevel,
		},
		"last_error":   encodeRuntimeError(auth.LastError),
		"model_states": modelStates,
	}

	setOrDeleteMetadataString(auth.Metadata, "last_error_kind", lastErrKind)
	setOrDeleteMetadataString(auth.Metadata, "last_error_reason", lastErrReason)
	setOrDeleteMetadataInt(auth.Metadata, "last_error_code", lastErrCode)
	setOrDeleteMetadataTime(auth.Metadata, "last_error_at", lastErrAt)
	setOrDeleteMetadataTime(auth.Metadata, "next_retry_after", auth.NextRetryAfter)
	setOrDeleteMetadataTime(auth.Metadata, "cooldown_until", cooldownUntil)
	auth.Metadata["disabled_by_policy"] = strings.HasPrefix(strings.TrimSpace(auth.StatusMessage), "disabled_by_policy:")
	auth.Metadata["status_message"] = strings.TrimSpace(auth.StatusMessage)
	auth.Metadata["status"] = string(auth.Status)
	auth.Metadata["unavailable"] = auth.Unavailable
	if lastErrKind != "" || lastErrReason != "" || lastErrCode > 0 || lastErrRetryable {
		auth.Metadata["last_error"] = map[string]any{
			"kind":      lastErrKind,
			"reason":    lastErrReason,
			"http_code": lastErrCode,
			"retryable": lastErrRetryable,
		}
	} else {
		delete(auth.Metadata, "last_error")
	}
}

func hydrateRuntimeStateFromMetadata(auth *Auth) {
	if auth == nil || auth.Metadata == nil {
		return
	}

	raw := auth.Metadata[runtimeStateMetadataKey]
	runtimeState, _ := anyToMap(raw)
	if runtimeState == nil {
		runtimeState = map[string]any{}
	}

	if status, ok := parseStatus(runtimeState["status"]); ok {
		auth.Status = status
	} else if status, ok := parseStatus(auth.Metadata["status"]); ok {
		auth.Status = status
	}
	if message := parseString(runtimeState["status_message"]); message != "" {
		auth.StatusMessage = message
	} else if message := parseString(auth.Metadata["status_message"]); message != "" {
		auth.StatusMessage = message
	}
	if unavailable, ok := parseBool(runtimeState["unavailable"]); ok {
		auth.Unavailable = unavailable
	} else if unavailable, ok := parseBool(auth.Metadata["unavailable"]); ok {
		auth.Unavailable = unavailable
	}
	if ts := parseTime(runtimeState["next_retry_after"]); !ts.IsZero() {
		auth.NextRetryAfter = ts
	} else if ts := parseTime(auth.Metadata["next_retry_after"]); !ts.IsZero() {
		auth.NextRetryAfter = ts
	}
	if ts := parseTime(runtimeState["updated_at"]); !ts.IsZero() {
		auth.UpdatedAt = ts
	}

	if quotaMap, _ := anyToMap(runtimeState["quota"]); quotaMap != nil {
		auth.Quota = decodeQuotaState(quotaMap)
	}
	if lastErrMap, _ := anyToMap(runtimeState["last_error"]); lastErrMap != nil {
		auth.LastError = decodeRuntimeError(lastErrMap)
	} else if lastErrMap, _ := anyToMap(auth.Metadata["last_error"]); lastErrMap != nil {
		auth.LastError = decodeRuntimeError(lastErrMap)
	}
	if auth.LastError == nil {
		kind := parseString(auth.Metadata["last_error_kind"])
		reason := parseString(auth.Metadata["last_error_reason"])
		code, _ := parseInt(auth.Metadata["last_error_code"])
		if kind != "" || reason != "" || code > 0 {
			auth.LastError = &Error{
				Code:       kind,
				Message:    reason,
				HTTPStatus: code,
			}
		}
	}

	if modelStatesAny, _ := anyToMap(runtimeState["model_states"]); modelStatesAny != nil {
		modelStates := make(map[string]*ModelState, len(modelStatesAny))
		for model, rawState := range modelStatesAny {
			stateMap, _ := anyToMap(rawState)
			if stateMap == nil {
				continue
			}
			state := &ModelState{}
			if status, ok := parseStatus(stateMap["status"]); ok {
				state.Status = status
			}
			state.StatusMessage = parseString(stateMap["status_message"])
			if unavailable, ok := parseBool(stateMap["unavailable"]); ok {
				state.Unavailable = unavailable
			}
			state.NextRetryAfter = parseTime(stateMap["next_retry_after"])
			state.UpdatedAt = parseTime(stateMap["updated_at"])
			if quotaMap, _ := anyToMap(stateMap["quota"]); quotaMap != nil {
				state.Quota = decodeQuotaState(quotaMap)
			}
			if errMap, _ := anyToMap(stateMap["last_error"]); errMap != nil {
				state.LastError = decodeRuntimeError(errMap)
			}
			modelStates[model] = state
		}
		if len(modelStates) > 0 {
			auth.ModelStates = modelStates
		}
	}
}

func encodeRuntimeError(err *Error) any {
	if err == nil {
		return nil
	}
	return map[string]any{
		"kind":      strings.TrimSpace(err.Code),
		"reason":    strings.TrimSpace(err.Message),
		"http_code": err.HTTPStatus,
		"retryable": err.Retryable,
	}
}

func decodeRuntimeError(raw map[string]any) *Error {
	if len(raw) == 0 {
		return nil
	}
	kind := parseString(raw["kind"])
	reason := parseString(raw["reason"])
	httpCode, _ := parseInt(raw["http_code"])
	retryable, _ := parseBool(raw["retryable"])
	if kind == "" && reason == "" && httpCode == 0 && !retryable {
		return nil
	}
	return &Error{
		Code:       kind,
		Message:    reason,
		HTTPStatus: httpCode,
		Retryable:  retryable,
	}
}

func decodeQuotaState(raw map[string]any) QuotaState {
	if len(raw) == 0 {
		return QuotaState{}
	}
	exceeded, _ := parseBool(raw["exceeded"])
	backoffLevel, _ := parseInt(raw["backoff_level"])
	return QuotaState{
		Exceeded:      exceeded,
		Reason:        parseString(raw["reason"]),
		NextRecoverAt: parseTime(raw["next_recover_at"]),
		BackoffLevel:  backoffLevel,
	}
}

func setOrDeleteMetadataString(metadata map[string]any, key, value string) {
	if metadata == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		delete(metadata, key)
		return
	}
	metadata[key] = value
}

func setOrDeleteMetadataInt(metadata map[string]any, key string, value int) {
	if metadata == nil {
		return
	}
	if value == 0 {
		delete(metadata, key)
		return
	}
	metadata[key] = value
}

func setOrDeleteMetadataTime(metadata map[string]any, key string, ts time.Time) {
	if metadata == nil {
		return
	}
	if ts.IsZero() {
		delete(metadata, key)
		return
	}
	metadata[key] = ts.UTC().Format(time.RFC3339Nano)
}

func formatRuntimeTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

func anyToMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	switch typed := v.(type) {
	case map[string]any:
		return typed, true
	default:
		raw, errMarshal := json.Marshal(v)
		if errMarshal != nil {
			return nil, false
		}
		out := map[string]any{}
		if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
			return nil, false
		}
		return out, true
	}
}

func parseString(v any) string {
	switch typed := v.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func parseBool(v any) (bool, bool) {
	switch typed := v.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return false, false
		}
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, false
		}
		return parsed, true
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	case int64:
		return typed != 0, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return false, false
		}
		return parsed != 0, true
	default:
		return false, false
	}
}

func parseInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func parseTime(v any) time.Time {
	switch typed := v.(type) {
	case time.Time:
		return typed
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}
		}
		if ts, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			return ts
		}
		if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return ts
		}
		return time.Time{}
	case float64:
		return time.Unix(int64(typed), 0).UTC()
	case int64:
		return time.Unix(typed, 0).UTC()
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return time.Unix(parsed, 0).UTC()
		}
		return time.Time{}
	default:
		return time.Time{}
	}
}

func parseStatus(v any) (Status, bool) {
	raw := strings.TrimSpace(parseString(v))
	if raw == "" {
		return "", false
	}
	switch Status(raw) {
	case StatusUnknown, StatusActive, StatusError, StatusDisabled:
		return Status(raw), true
	default:
		return "", false
	}
}
