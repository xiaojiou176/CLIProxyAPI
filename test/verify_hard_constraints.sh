#!/usr/bin/env bash
set -euo pipefail

# Equilibrium-style hard constraints verification for CLIProxyAPI.
#
# What it verifies:
# 1) Session sticks to same auth until blocked.
# 2) Failed auth has >=30m cooldown and frozen auth is skipped.
# 3) Fatal auth state survives reload (persisted runtime state).
# 4) Codex stream terminal/failure events are classified correctly.
# 5) (Optional) Runtime deployed binary hash matches freshly built binary.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

RUNTIME_BIN_PATH_DEFAULT="$HOME/Library/Application Support/Quotio/CLIProxyAPI"
RUNTIME_BIN_PATH="${RUNTIME_BIN_PATH:-$RUNTIME_BIN_PATH_DEFAULT}"
CHECK_RUNTIME_HASH="${CHECK_RUNTIME_HASH:-1}"

echo "[1/6] Build server"
go build ./cmd/server

echo "[2/6] Verify sticky selection + session affinity"
go test -count=1 ./sdk/cliproxy/auth -run 'TestRoundRobinSelectorPick_SticksToCurrentUntilBlocked|TestRoundRobinSelectorPick_SessionAffinitySticksToSameAuth|TestRoundRobinSelectorPick_SessionAffinityFailsOverAndRebinds'

echo "[3/6] Verify minimum cooldown >=30m + skip frozen auth"
go test -count=1 ./sdk/cliproxy/auth -run 'TestManager_MarkResult_EnforcesMinimumFailureCooldown|TestMarkResult_QuotaFreezeBlockUsesMinimumCooldown|TestPickNextMixed_SkipsRecentlyFrozenAuth|TestRefreshAuth_PreservesActiveCooldownRuntimeState'

echo "[4/6] Verify fatal disable classification + runtime persistence"
go test -count=1 ./sdk/cliproxy/auth -run 'TestClassifyResultError_TokenInvalidatedIsFatal|TestRuntimeStatePersistence_RoundTripCooldownAndModelState|TestRuntimeStatePersistence_RoundTripFatalDisable'

echo "[5/6] Verify Codex stream terminal + failure parsing"
go test -count=1 ./internal/runtime/executor -run 'TestCodexTerminalEventType|TestCodexTerminalPayload|TestCodexStreamFailure|TestCodexDisconnectedStreamErr'

echo "[6/6] Verify runtime binary hash parity (optional)"
if [[ "$CHECK_RUNTIME_HASH" == "1" ]]; then
  if [[ -f "$RUNTIME_BIN_PATH" ]]; then
    SRC_HASH="$(shasum -a 256 "$ROOT_DIR/server" | awk '{print $1}')"
    RUNTIME_HASH="$(shasum -a 256 "$RUNTIME_BIN_PATH" | awk '{print $1}')"

    echo "  source : $SRC_HASH"
    echo "  runtime: $RUNTIME_HASH"

    if [[ "$SRC_HASH" != "$RUNTIME_HASH" ]]; then
      echo "❌ runtime binary is stale (hash mismatch)."
      exit 1
    fi
    echo "✅ runtime binary hash matched."
  else
    echo "⚠️ runtime binary not found at: $RUNTIME_BIN_PATH"
    echo "   skip hash check (set CHECK_RUNTIME_HASH=0 to silence this warning)."
  fi
else
  echo "  skipped (CHECK_RUNTIME_HASH=0)"
fi

echo "✅ hard constraints verification passed."
