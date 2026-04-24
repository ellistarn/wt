.PHONY: build test vet presubmit

presubmit: vet build test

build:
	go build ./...

vet:
	go vet ./...

test:
	go test -v ./...
