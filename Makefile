# Talon — build, test and packaging.
#
# Every target works offline: the project has no third-party Go dependencies.

APP      := talon
PKG      := github.com/CRISTOP-bot/talon/cmd/talon
BIN_DIR  := bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
# Package versions must start with a digit, while git tags conventionally start
# with "v". Strip it where the format demands it.
PKG_VERSION := $(patsubst v%,%,$(VERSION))
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X github.com/CRISTOP-bot/talon/internal/app.Version=$(VERSION) \
            -X github.com/CRISTOP-bot/talon/internal/app.Commit=$(COMMIT) \
            -X github.com/CRISTOP-bot/talon/internal/app.Date=$(DATE)
GOFLAGS  := -trimpath

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build ./bin/talon with version information
	@mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(APP) $(PKG)
	@echo "built $(BIN_DIR)/$(APP) $(VERSION)"

.PHONY: install
install: ## Install talon into $(GOPATH)/bin
	go install $(GOFLAGS) -ldflags '$(LDFLAGS)' $(PKG)
	@echo "installed $$(go env GOPATH)/bin/$(APP)"

.PHONY: run
run: build ## Run from source: make run ARGS="--help"
	./$(BIN_DIR)/$(APP) $(ARGS)

.PHONY: test
test: ## Run every test (unit + integration, no network required)
	go test ./...

.PHONY: test-race
test-race: ## Run the suite with the race detector
	go test -race ./...

.PHONY: test-short
test-short: ## Skip the slow integration tests
	go test -short ./...

.PHONY: cover
cover: ## Show coverage per package
	go test -cover ./...

.PHONY: bench
bench: ## Run benchmarks
	go test -bench=. -run='^$$' ./...

.PHONY: lint
lint: ## Check formatting and run go vet
	@out=$$(gofmt -l cmd internal examples); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go vet ./...
	@echo "lint ok"

.PHONY: fmt
fmt: ## Format the code
	gofmt -w cmd internal examples

.PHONY: tidy
tidy: ## Tidy go.mod (there are no third-party dependencies)
	go mod tidy

.PHONY: cross
cross: ## Build for linux, darwin and windows on amd64 and arm64
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		out=dist/$(APP)-$(VERSION)-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "  $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $$out $(PKG) || exit 1; \
	done
	@echo "artifacts in dist/"

.PHONY: checksums
checksums: cross ## Write dist/checksums.txt
	@cd dist && sha256sum $(APP)-* > checksums.txt && cat checksums.txt

.PHONY: deb
deb: build ## Build a Debian package (requires dpkg-deb)
	@mkdir -p dist/pkg/DEBIAN dist/pkg/usr/bin
	cp $(BIN_DIR)/$(APP) dist/pkg/usr/bin/$(APP)
	@sed -e 's/@VERSION@/$(PKG_VERSION)/' packaging/deb-control.in > dist/pkg/DEBIAN/control
	dpkg-deb --build --root-owner-group dist/pkg dist/$(APP)_$(PKG_VERSION)_linux_amd64.deb
	@echo "built dist/$(APP)_$(PKG_VERSION)_linux_amd64.deb"

.PHONY: rpm
rpm: build ## Build an RPM package (requires rpmbuild)
	rpmbuild -bb packaging/rpm.spec \
		--define "_version $(PKG_VERSION)" \
		--define "_sourcedir $(PWD)/$(BIN_DIR)" \
		--define "_rpmdir $(PWD)/dist"
	@echo "built an rpm in dist/"

.PHONY: appimage
appimage: build ## Package an AppImage layout under dist/appimage
	@mkdir -p dist/appimage/usr/bin dist/appimage/usr/share/applications
	cp $(BIN_DIR)/$(APP) dist/appimage/usr/bin/$(APP)
	sed -e 's|@EXEC@|/usr/bin/$(APP)|' packaging/talon.desktop.in \
		> dist/appimage/usr/share/applications/$(APP).desktop
	@echo "AppImage payload in dist/appimage (use appimagetool to seal it)"

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN_DIR) dist

.PHONY: verify
verify: lint test ## Lint and test everything
	@echo "verified"
