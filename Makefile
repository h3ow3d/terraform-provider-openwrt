SHELL := /bin/bash
GO ?= $(shell command -v go 2>/dev/null || echo /opt/homebrew/bin/go)

.PHONY: tidy test build fmt precommit testacc-mock

tidy:
	$(GO) mod tidy

test:
	$(GO) test ./...

build:
	$(GO) build ./...

fmt:
	gofmt -w $(shell find . -name '*.go')

precommit:
	pre-commit run --all-files

testacc-mock:
	TF_ACC=1 OPENWRT_ACC_TARGET=mock $(GO) test -v ./internal/resources/domain ./internal/resources/dhcphost ./internal/resources/dhcppool -run TestAccOpenWRT.*MockLifecycle -count=1
