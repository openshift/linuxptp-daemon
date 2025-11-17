# Check if .env file exists and include it
ifneq (,$(wildcard ./.env))
    include ./.env
    export
endif

IMAGE_NAME ?= linuxptp-daemon-image
IMAGE_TAG_BASE ?= ghcr.io/k8snetworkplumbingwg/$(IMAGE_NAME)
VERSION ?=latest
IMG ?= $(IMAGE_TAG_BASE):$(VERSION)
CONTAINER_TOOL ?=docker

.PHONY: test
default:
	./hack/build.sh
image:
	./hack/build-image.sh
push:
	$(CONTAINER_TOOL) push $(IMG)
clean:
	./hack/cleanup.sh
fmt:
	./hack/gofmt.sh

test:
	SKIP_GNSS_MONITORING=1 go test ./... --tags=unittests -coverprofile=cover.out

lint:
	golangci-lint run
