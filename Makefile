SHELL := /bin/bash

.PHONY: tidy test build fmt precommit

tidy:
	go mod tidy

test:
	go test ./...

build:
	go build ./...

fmt:
	gofmt -w $(shell find . -name '*.go')

precommit:
	pre-commit run --all-files
