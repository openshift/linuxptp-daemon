#!/bin/bash
# Run unit tests and generate filtered coverage profile.
set -e
SKIP_GNSS_MONITORING=1 go test ./... --tags=unittests -coverprofile=coverage.raw.out
grep -vE "zz_generated|\.pb\.go|mock_" coverage.raw.out > coverage.out
