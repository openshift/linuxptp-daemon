.PHONY: test
default:
	./hack/build.sh
image:
	./hack/build-image.sh
push:
	docker push ghcr.io/k8snetworkplumbingwg/linuxptp-daemon-image:latest
clean:
	./hack/cleanup.sh
fmt:
	./hack/gofmt.sh

test:
	go test ./... --tags=unittests -coverprofile=cover.out

lint:
	golangci-lint run