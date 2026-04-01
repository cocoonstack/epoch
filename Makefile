.PHONY: build test lint fmt vet ci clean serve docker-mysql docker-build

BINARY := epoch
GOFLAGS := -trimpath
LDFLAGS := -s -w

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) .

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

fmt:
	gofumpt -w .
	goimports -w .

vet:
	go vet ./...

ci: vet test build

clean:
	rm -f $(BINARY)

# --- Server ---

serve:
	go run . serve --dsn "epoch:epoch@tcp(127.0.0.1:3306)/epoch?parseTime=true"

docker-mysql:
	cd deploy && docker compose up -d mysql

docker-build:
	docker build -t epoch-server -f deploy/Dockerfile .
