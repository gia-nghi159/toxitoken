# Toxitoken Makefile

.PHONY: build run test clean

# Default target
all: build

# Automatically load .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# Build both binaries
build:
	@echo "Building gateway server..."
	@go build -o bin/server ./cmd/server
	@echo "Building toxi CLI..."
	@go build -o bin/toxi ./cmd/toxi
	@echo "Done! Binaries are in the bin/ directory."

# Run the server locally
run: build
	@echo "Starting Toxitoken gateway..."
	@./bin/server

# Run the test suite
test:
	@go test -v -race ./...

# Clean the bin directory
clean:
	@rm -rf bin/
