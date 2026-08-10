VERSION ?= v1.3.14
NAME := talkeq

# Every target here is a command, not a file. Without this, a target sharing a
# name with a directory in the repo is silently treated as already built - which
# is exactly what happened when the sanitize package was added and quietly
# disabled the sanitize target.
.PHONY: run run-hub run-agent sanitize lint test test-race build-all build-prepare \
	build-linux build-darwin build-windows build-linux-arm analyze coverage \
	profile-heap profile-trace

# run a copy of talkeq
run:
	@echo "run: building"
	@mkdir -p bin
	cd bin && go run ..

# run the relay hub
run-hub:
	@mkdir -p bin
	cd bin && go run ../cmd/talkeq-hub

# run a relay agent
run-agent:
	@mkdir -p bin
	cd bin && go run ../cmd/talkeq-agent

# vet and test, the checks that need no extra tooling
sanitize: lint test-race

# static analysis. staticcheck and goimports are optional so a contributor
# without them can still build and test; golint and gocyclo are gone because
# golint was archived in 2021 and staticcheck covers the same ground.
# The vet invocation names tlog's printf-style wrappers. vet cannot see into
# them otherwise, which is exactly why several format-string bugs survived in
# this codebase for so long.
lint:
	@echo "lint: go vet"
	@go vet -printf.funcs=Debugf,Infof,Warnf,Errorf,Fatalf,Panicf ./...
	@echo "lint: gofmt"
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files above" && exit 1)
	@if command -v staticcheck >/dev/null 2>&1; then \
		echo "lint: staticcheck"; \
		staticcheck ./...; \
	else \
		echo "lint: staticcheck not installed, skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"; \
	fi

# do tests against the codebase
test:
	@go test -cover ./...

# tests with the race detector, which is what CI runs
test-race:
	@go test -race ./...

# test with a coverage summary
coverage:
	@go test -covermode=atomic -coverprofile=coverage.out ./...
	@go tool cover -func coverage.out | grep total

# talkeq is the original single-server binary. talkeq-hub and talkeq-agent are
# the two halves of the cross-server relay.
BINARIES := talkeq talkeq-hub talkeq-agent

# build all supported versions
build-all: build-prepare build-linux build-darwin build-windows

# prep for building
build-prepare:
	@echo "Preparing talkeq ${VERSION}"
	@rm -rf bin/*
	@-mkdir -p bin/


# pkg-for resolves a binary name to the package that builds it. The original
# talkeq lives at the module root; the relay binaries live under cmd/.
pkg-for = $(if $(filter talkeq,$(1)),.,./cmd/$(1))

# make darwin binaries
build-darwin:
	@echo "build-darwin: building ${VERSION}"
	@$(foreach bin,$(BINARIES), \
		GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -buildmode=pie -ldflags="-X main.Version=${VERSION} -s -w" -o bin/$(bin)-darwin $(call pkg-for,$(bin)) &&) true

# make linux binaries
build-linux:
	@echo "build-linux: building ${VERSION}"
	@$(foreach bin,$(BINARIES), \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-X main.Version=${VERSION} -s -w" -o bin/$(bin)-linux $(call pkg-for,$(bin)) &&) true

# make windows binaries
build-windows:
	@echo "build-windows: building ${VERSION}"
	@$(foreach bin,$(BINARIES), \
		GOOS=windows GOARCH=amd64 go build -buildmode=pie -ldflags="-X main.Version=${VERSION} -s -w" -o bin/$(bin)-windows.exe $(call pkg-for,$(bin)) &&) true

build-linux-arm:
	@echo "Building Linux-arm ${VERSION}"
	@$(foreach bin,$(BINARIES), \
		GOOS=linux GOARCH=arm go build -ldflags="-X main.Version=${VERSION} -w" -o bin/$(bin)-linux-arm $(call pkg-for,$(bin)) &&) true

# analyze the binary using binskim
analyze:
	binskim analyze bin/${NAME}-linux

# CICD triggers this
set-version-%:
	@echo "VERSION=${VERSION}.$*" >> $$GITHUB_ENV

# run pprof and dump 3 snapshots of heap
profile-heap:
	@echo "profile-heap: running pprof watcher for 2 minutes with snapshots 0 to 3..."
	@-mkdir -p bin
	curl http://localhost:8082/debug/pprof/heap > bin/heap.0.pprof
	sleep 30
	curl http://localhost:8082/debug/pprof/heap > bin/heap.1.pprof
	sleep 30
	curl http://localhost:8082/debug/pprof/heap > bin/heap.2.pprof
	sleep 30
	curl http://localhost:8082/debug/pprof/heap > bin/heap.3.pprof

# peek at a heap
profile-heap-%:
	@echo "profile-heap-$*: use top20, svg, or list *word* for pprof commands, ctrl+c when done"
	go tool pprof bin/heap.$*.pprof

# run a trace on quail
profile-trace:
	@echo "profile-trace: getting trace data, this can show memory leaks and other issues..."
	curl http://localhost:8082/debug/pprof/trace > bin/trace.out
	go tool trace bin/trace.out
