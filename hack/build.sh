#!/bin/bash

ORG_PATH="github.com/openshift"
REPO_PATH="${ORG_PATH}/linuxptp-daemon"

if [ ! -h .gopath/src/${REPO_PATH} ]; then
        mkdir -p .gopath/src/${ORG_PATH}
        ln -s ../../../.. .gopath/src/${REPO_PATH} || exit 255
fi

export GO15VENDOREXPERIMENT=1
export GOBIN=${PWD}/bin
export GOPATH=${PWD}/.gopath
go build --mod=vendor "$@" -o bin/ptp ${REPO_PATH}/cmd
