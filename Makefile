.PHONY: test
default:
	./hack/build.sh
image:
	./hack/build-image.sh
clean:
	./hack/cleanup.sh
fmt:
	./hack/gofmt.sh

test:
	SKIP_GNSS_MONITORING=1 go test ./... --tags=unittests -coverprofile=cover.out

lint:
	golangci-lint run