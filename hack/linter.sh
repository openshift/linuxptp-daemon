#!/bin/bash

# Check if golangci-lint is installed
if ! command -v golangci-lint &> /dev/null; then
  echo "golangci-lint is not installed. Please install it to run the linter."
  echo "See https://golangci-lint.run/welcome/install/ for installation instructions."
  exit 1
fi

# Get current version
LINT_VERSION=$(golangci-lint --version | sed -E 's/.*version ([^ ]+).*/\1/')
CLEAN_LINT=${LINT_VERSION#v}

# Get expected CI version
EXPECTED_VERSION=""
WORKFLOW_FILE=".github/workflows/linter.yml"
if [[ -f "$WORKFLOW_FILE" ]]; then
  EXPECTED_VERSION=$(grep -A 5 "golangci/golangci-lint-action" "$WORKFLOW_FILE" | grep "version:" | awk '{print $2}' | tr -d ' "')
fi

# Fail if version is 1.x
if [[ $CLEAN_LINT == 1.* ]]; then
  ERROR_MSG="Error: golangci-lint version 1.x is no longer supported (detected $LINT_VERSION). Please upgrade to 2.x"
  if [[ -n "$EXPECTED_VERSION" ]]; then
    ERROR_MSG+=" (CI expects $EXPECTED_VERSION)"
  fi
  echo "$ERROR_MSG."
  exit 1
fi

# Compare with CI version
if [[ -n "$EXPECTED_VERSION" ]]; then
  CLEAN_EXPECTED=${EXPECTED_VERSION#v}
  if [[ $CLEAN_LINT != $CLEAN_EXPECTED* ]]; then
    echo "Warning: Local golangci-lint version ($LINT_VERSION) is out-of-sync with main CI version ($EXPECTED_VERSION)."
  fi
fi

golangci-lint run "$@"
