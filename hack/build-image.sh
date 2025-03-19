#!/bin/bash

CONTAINER_TOOL="${CONTAINER_TOOL:-docker}"
IMAGE_NAME="${IMAGE_NAME:-linuxptp-daemon-image}"
IMAGE_TAG_BASE="${IMAGE_TAG_BASE:-ghcr.io/k8snetworkplumbingwg/${IMAGE_NAME}}"
VERSION="${VERSION:-latest}"
IMG="${IMAGE_TAG_BASE}:${VERSION}"

$CONTAINER_TOOL build -t "${IMG}"  -f ./Dockerfile .
