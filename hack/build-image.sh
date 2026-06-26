#!/bin/bash

CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"
IMAGE_NAME="${IMAGE_NAME:-linuxptp-daemon-image}"
IMAGE_TAG_BASE="${IMAGE_TAG_BASE:-ghcr.io/k8snetworkplumbingwg/${IMAGE_NAME}}"
VERSION="${VERSION:-latest}"
IMG="${IMAGE_TAG_BASE}:${VERSION}"

GIT_COMMIT="${GIT_COMMIT:-$(git rev-list -1 HEAD 2>/dev/null || echo unknown)}"
$CONTAINER_TOOL build --build-arg GIT_COMMIT="${GIT_COMMIT}" -t "${IMG}" -f ./Dockerfile .
