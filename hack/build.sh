#!/bin/bash

ORG_PATH="github.com/k8snetworkplumbingwg"
REPO_PATH="${ORG_PATH}/linuxptp-daemon"

# GIT_COMMIT can be set via:
#   1. Environment variable (e.g. Dockerfile ARG/ENV)
#   2. Git repository (local builds)
#   3. Falls back to "unknown"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-list -1 HEAD 2>/dev/null || echo unknown)}"
LINKER_RELEASE_FLAGS="-X main.GitCommit=${GIT_COMMIT}"
go build -ldflags "${LINKER_RELEASE_FLAGS}" --mod=vendor "$@" -o bin/ptp ${REPO_PATH}/cmd
