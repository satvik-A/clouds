# CloudFS Makefile

# SQLCipher build flags for macOS
CGO_ENABLED := 1
CGO_CFLAGS := -I/opt/homebrew/opt/sqlcipher/include
CGO_LDFLAGS := -L/opt/homebrew/opt/sqlcipher/lib -lsqlcipher
BUILD_TAGS := sqlcipher

# Export CGO flags
export CGO_ENABLED
export CGO_CFLAGS
export CGO_LDFLAGS

.PHONY: all build test test-verbose clean

all: build

build:
	go build -tags $(BUILD_TAGS) -o cloudfs ./cmd/cloudfs

test:
	go test -tags $(BUILD_TAGS) ./internal/core/...

test-verbose:
	go test -v -tags $(BUILD_TAGS) ./internal/core/...

test-crypto:
	go test -v -tags $(BUILD_TAGS) ./internal/core/... -run Encrypt

test-failure:
	go test -v -tags $(BUILD_TAGS) ./internal/core/... -run "Crash|Interrupt|Partial|Recovery|Integrity"

clean:
	rm -f cloudfs
	rm -f coverage.out

coverage:
	go test -tags $(BUILD_TAGS) -coverprofile=coverage.out ./internal/...
	go tool cover -html=coverage.out

install:
	go install -tags $(BUILD_TAGS) ./cmd/cloudfs

# Development helpers
run:
	go run -tags $(BUILD_TAGS) ./cmd/cloudfs $(ARGS)

help:
	@echo "CloudFS Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  build        - Build cloudfs binary"
	@echo "  test         - Run all tests"
	@echo "  test-verbose - Run tests with verbose output"
	@echo "  test-crypto  - Run SQLCipher tests"
	@echo "  test-failure - Run failure simulation tests"
	@echo "  coverage     - Generate coverage report"
	@echo "  clean        - Remove built artifacts"
	@echo "  install      - Install cloudfs to GOPATH/bin"
	@echo ""
	@echo "Prerequisites:"
	@echo "  brew install sqlcipher"
