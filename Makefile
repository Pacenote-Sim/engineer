# Pacenote engineer plugin — developer entry points. Every target is what CI runs.
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
export PATH := $(PATH):$(shell go env GOPATH)/bin

# The tools, each named once. The PATH above is not enough on its own: GNU Make
# runs a recipe line directly, without a shell, when the line holds no shell
# metacharacters, and that direct execution searches make's own PATH. A tool
# installed by `go install` is invisible to exactly the recipes that are a
# single command, so each is resolved here.
GOBIN        := $(shell go env GOPATH)/bin
GOFUMPT      := $(shell command -v gofumpt       2>/dev/null || echo $(GOBIN)/gofumpt)
GOLANGCILINT := $(shell command -v golangci-lint 2>/dev/null || echo $(GOBIN)/golangci-lint)
GOVULNCHECK  := $(shell command -v govulncheck   2>/dev/null || echo $(GOBIN)/govulncheck)

TESTFLAGS := -race -shuffle=on -count=1
COVER_MIN := 90
DIST      := dist/engineer

# The store's tests run against a real PostgreSQL, in schemas of their own that
# they drop afterwards. Point this at a server you may create schemas on:
#   make test-postgres PACENOTE_TEST_DATABASE_URL=postgres://you@localhost:5432/postgres
PACENOTE_TEST_DATABASE_URL ?=

.DEFAULT_GOAL := check

.PHONY: help check build vet lint lint-fix fmt fmt-check test test-postgres cover bench bench-smoke tidy-check vuln dist clean

## help: list targets
help:
	@grep -E '^## [a-z-]+:' $(MAKEFILE_LIST) | sed -E 's/^## ([a-z-]+): */\1\t/' | column -t -s $$'\t'

## check: everything CI runs that needs no database, in order
check: fmt-check build vet lint test bench-smoke tidy-check

## build: compile the plugin
build:
	go build ./...

## vet: go vet, with and without the database tests
vet:
	go vet ./...
	go vet -tags postgres ./...

## lint: golangci-lint, the database tests included
lint:
	$(GOLANGCILINT) run --build-tags postgres ./...

## lint-fix: golangci-lint, fixing what it can
lint-fix:
	$(GOLANGCILINT) run --build-tags postgres --fix ./...

## fmt: gofumpt in place
fmt:
	$(GOFUMPT) -w .

## fmt-check: fail if anything is unformatted
fmt-check:
	@out=$$($(GOFUMPT) -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

## test: the suite without a database, with the race detector
test:
	go test $(TESTFLAGS) ./...

## test-postgres: the whole suite, the store included, against PACENOTE_TEST_DATABASE_URL
test-postgres:
	@if [ -z "$(PACENOTE_TEST_DATABASE_URL)" ]; then \
		echo "set PACENOTE_TEST_DATABASE_URL to a PostgreSQL server you may create schemas on"; exit 1; fi
	PACENOTE_TEST_DATABASE_URL=$(PACENOTE_TEST_DATABASE_URL) go test -tags postgres $(TESTFLAGS) ./...

## cover: coverage with the store — every package over $(COVER_MIN)%; needs PACENOTE_TEST_DATABASE_URL
cover:
	@if [ -z "$(PACENOTE_TEST_DATABASE_URL)" ]; then \
		echo "set PACENOTE_TEST_DATABASE_URL to a PostgreSQL server you may create schemas on"; exit 1; fi
	PACENOTE_TEST_DATABASE_URL=$(PACENOTE_TEST_DATABASE_URL) \
		go test -tags postgres -covermode=atomic -coverprofile=coverage.out ./...
	scripts/coverage.sh coverage.out $(COVER_MIN)

## bench: what a lap costs this plugin besides the model
bench:
	go test -run XXX -bench . -benchmem ./...

## bench-smoke: run each benchmark once, so they cannot rot unnoticed
bench-smoke:
	go test -run XXX -bench . -benchtime=1x ./...

## tidy-check: fail if go.mod or go.sum would change, as a checkout outside the workspace sees them
tidy-check:
	GOWORK=off go mod tidy -diff

## vuln: govulncheck
vuln:
	$(GOVULNCHECK) ./...

## dist: the folder to drop into the server's plugin directory, as dist/engineer
dist: build
	rm -rf $(DIST)
	mkdir -p $(DIST)
	go build -trimpath -o $(DIST)/engineer ./cmd/engineer
	cp plugin.json $(DIST)/
	cp -R migrations $(DIST)/

## clean: remove build outputs
clean:
	rm -rf dist coverage.out
