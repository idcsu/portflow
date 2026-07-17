VERSION ?= dev
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.agentVersion=$(VERSION)

.PHONY: build test fmt web-build preflight installer-test

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "$(LDFLAGS)" -o bin/portflow-control ./cmd/control
	CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "$(LDFLAGS)" -o bin/portflow-agent ./cmd/agent

test:
	go test ./...

fmt:
	gofmt -w $$(find cmd internal -name '*.go' -type f)

web-build:
	npm --prefix web run build

preflight:
	./scripts/preflight.sh $${ENV_FILE:-.env.production}

installer-test:
	bash -n install.sh
	./scripts/install_test.sh
