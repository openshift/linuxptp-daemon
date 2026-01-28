#!/bin/bash

ORG_PATH="github.com/k8snetworkplumbingwg"
REPO_PATH="${ORG_PATH}/linuxptp-daemon"

GIT_COMMIT=$(git rev-list -1 HEAD)
LINKER_RELEASE_FLAGS="-X main.GitCommit=${GIT_COMMIT}"
go build -ldflags "${LINKER_RELEASE_FLAGS}" --mod=vendor "$@" -o bin/ptp ${REPO_PATH}/cmd
