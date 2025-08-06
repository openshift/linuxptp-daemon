.PHONY: test
default:
	./hack/build.sh
image:
	./hack/build-image.sh
clean:
	./hack/cleanup.sh
fmt:
	./hack/gofmt.sh
leapfile:
	wget https://www.ietf.org/timezones/data/leap-seconds.list -O ./extra/leap-seconds.list

test:
	SKIP_GNSS_MONITORING=1 go test ./... --tags=unittests -coverprofile=cover.out

lint:
	golangci-lint run
