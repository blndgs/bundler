# Go parameters
GOCMD := go
GOBUILD := $(GOCMD) build
GOCLEAN := $(GOCMD) clean
GOTEST := $(GOCMD) test
GOGET := $(GOCMD) get
BINARY_NAME := bundler
BINARY_UNIX := $(BINARY_NAME)_unix

# Targets
all: test build

build:
	$(GOBUILD) -o $(BINARY_NAME) -v cmd/main.go

test:
	$(GOTEST) -v ./...

clean:
	$(GOCLEAN)
	rm -f $(BINARY_NAME)

run-prv:
	$(GOCMD) run cmd/main.go start --mode private

run-mev:
	$(GOCMD) run cmd/main.go start --mode searcher

rm-db:
	rm -rf /tmp/balloondogs_db

.PHONY: all build test clean run-prv run-mev rm-db