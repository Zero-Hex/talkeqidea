VERSION ?= v1.3.14
NAME := talkeq

# run a copy of talkeq
run: sanitize
	@echo "run: building"
	mkdir -p bin
	cd bin && go run ../main.go

# clean up and check for errors
sanitize:
	@echo "sanitize: checking for errors"
	rm -rf vendor/
	go vet -tags ci ./...
	test -z $(goimports -e -d . | tee /dev/stderr)
	gocyclo -over 30 .
	golint -set_exit_status $(go list -tags ci ./...)
	staticcheck -go 1.14 ./...
	go test -tags ci -covermode=atomic -coverprofile=coverage.out ./...
    coverage=`go tool cover -func coverage.out | grep total | tr -s '\t' | cut -f 3 | grep -o '[^%]*'`

# do tests against the codebase
test:
	@go test -cover ./...

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
