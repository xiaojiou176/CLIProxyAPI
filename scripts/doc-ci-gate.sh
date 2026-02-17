#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CHANGED_FILES=()
if [[ -n "${GITHUB_BASE_REF:-}" ]]; then
  BASE_REF="origin/${GITHUB_BASE_REF}"
  git fetch --no-tags origin "${GITHUB_BASE_REF}" >/dev/null 2>&1 || true
  RANGE="${BASE_REF}...HEAD"
  DIFF_CMD=(git diff --name-only "$RANGE")
elif [[ -z "${CI:-}" ]]; then
  DIFF_CMD=(git diff --name-only HEAD)
elif git rev-parse --verify HEAD~1 >/dev/null 2>&1; then
  RANGE="HEAD~1...HEAD"
  DIFF_CMD=(git diff --name-only "$RANGE")
else
  DIFF_CMD=(git diff --name-only HEAD)
fi
while IFS= read -r line; do
  CHANGED_FILES+=("$line")
done < <("${DIFF_CMD[@]}")
while IFS= read -r line; do
  CHANGED_FILES+=("$line")
done < <(git ls-files --others --exclude-standard)

if [[ ${#CHANGED_FILES[@]} -eq 0 ]]; then
  echo "doc-ci-gate: no changed files detected, skip."
  exit 0
fi

changed() {
  local pattern="$1"
  local f
  for f in "${CHANGED_FILES[@]}"; do
    [[ "$f" == $pattern ]] && return 0
  done
  return 1
}

changed_any() {
  local pattern
  for pattern in "$@"; do
    changed "$pattern" && return 0
  done
  return 1
}

failures=()
LOCAL_NON_STRICT=0
if [[ -z "${GITHUB_BASE_REF:-}" && -z "${CI:-}" && "${DOC_GATE_STRICT:-0}" != "1" ]]; then
  LOCAL_NON_STRICT=1
fi

for required in "README.md" "AGENTS.md" "CLAUDE.md" "docs/documentation-policy.md"; do
  [[ -f "$required" ]] || failures+=("missing required file: $required")
done

if [[ $LOCAL_NON_STRICT -eq 0 ]]; then
  if changed_any "internal/*" "internal/**" "sdk/*" "sdk/**" "cmd/*" "cmd/**"; then
    if ! changed_any "README.md" "docs/*" "docs/**" "AGENTS.md" "CLAUDE.md"; then
      failures+=("code changed under internal/sdk/cmd but no docs updated")
    fi
  fi

  if changed_any ".github/workflows/*" ".github/workflows/**"; then
    changed "docs/documentation-policy.md" || failures+=("workflow changed but docs/documentation-policy.md not updated")
  fi
fi

if (( ${#failures[@]} > 0 )); then
  echo "doc-ci-gate: FAILED"
  printf ' - %s\n' "${failures[@]}"
  exit 1
fi

if [[ $LOCAL_NON_STRICT -eq 1 ]]; then
  echo "doc-ci-gate: PASS (local non-strict mode; set DOC_GATE_STRICT=1 for full contract checks)"
else
  echo "doc-ci-gate: PASS"
fi
