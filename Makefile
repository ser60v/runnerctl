SHELL := /bin/sh
GOCACHE ?= $(CURDIR)/.runtime/go-build
GOMODCACHE ?= $(CURDIR)/.runtime/go-mod
BUILD_DIR ?= $(CURDIR)/.build
GO_ENV := GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) GOTOOLCHAIN=local
CLI_VERSION := $(shell cat VERSION)
COMPOSE := env -u SSH_AUTH_SOCK CLI_VERSION=$(CLI_VERSION) docker compose --env-file .env

.PHONY: env fmt check build dev up down smoke
env:
	@sh scripts/ensure-env.sh

fmt: env
	@gofmt -w $$(find cmd internal tools -name '*.go' -type f)

check: env
	@sh scripts/check-repository.sh
	@mkdir -p "$(GOCACHE)" "$(GOMODCACHE)"
	@$(GO_ENV) go mod download
	@$(GO_ENV) go mod verify
	@test -z "$$(gofmt -l $$(find cmd internal tools -name '*.go' -type f))"
	@$(GO_ENV) go vet ./...
	@$(GO_ENV) go test ./...
	@$(GO_ENV) go test -race ./...
	@python3 tests/test_installers.py
	@$(MAKE) --no-print-directory build

build: env
	@mkdir -p "$(BUILD_DIR)"
	@$(GO_ENV) CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=$(CLI_VERSION)" -o "$(BUILD_DIR)/runnerctl" ./cmd/runnerctl

dev: env
	@$(GO_ENV) go run ./cmd/runnerctl help

up: env
	@$(COMPOSE) run --build --rm runnerctl version

down: env
	@$(COMPOSE) down --remove-orphans

smoke: env build
	@"$(BUILD_DIR)/runnerctl" --json version
	@env SSH_AUTH_SOCK= $(COMPOSE) config --quiet
