export PKG_CONFIG_PATH := $(CURDIR)/third_party/install/lib/pkgconfig

.PHONY: libacars test build
libacars:
	scripts/build-libacars.sh
test:
	go vet ./... && go test ./...
build:
	go build -o bin/ ./cmd/...
