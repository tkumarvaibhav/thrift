VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test eval fmt clean install

build:
	go build -ldflags '$(LDFLAGS)' -o bin/thrift ./cmd/thrift

test:
	go test ./...

eval: build
	bash evals/run.sh

fmt:
	gofmt -w .

clean:
	rm -rf bin

# The plugin ships source, not a binary: one static build per platform is more
# release machinery than a hook wrapper that fails open needs. Until bin/thrift
# exists the hook exits silently and nothing is bounded.
install: build
	@echo
	@echo "Built bin/thrift ($(VERSION))."
	@echo "Add this directory as a plugin marketplace, then:"
	@echo "  claude plugin install thrift@thrift"
	@echo "Verify with:  /thrift:doctor"
