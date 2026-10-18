BINARY := bin/onyx-ingestor
GO ?= go

.PHONY: build test vet fmt tidy lint secrets hooks run clean

build:
	$(GO) build -trimpath -o $(BINARY) ./cmd/onyx-ingestor

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy -diff

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed; skipping"; fi

secrets:
	@if command -v gitleaks >/dev/null 2>&1; then gitleaks git --config .gitleaks.toml --verbose .; else echo "gitleaks not installed; skipping (brew install gitleaks)"; fi

hooks:
	git config core.hooksPath .githooks && echo "git hooks enabled (.githooks/pre-commit runs gitleaks on staged changes)"

run: build
	./$(BINARY) version

clean:
	rm -rf bin
