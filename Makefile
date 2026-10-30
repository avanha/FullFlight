export PKG_CONFIG_PATH := $(CURDIR)/third_party/install/lib/pkgconfig

.PHONY: libacars test build
libacars:
	scripts/build-libacars.sh
test:
	go vet ./... && go test ./...
build:
	go build -o bin/ ./cmd/...

# Behavioural check of the web page script (needs node); not part of `make test`.
.PHONY: uitest
uitest:
	node scripts/uitest.js
	node scripts/uitest-notes.js

# Download the optional waypoint database (GPL, 2013; see the README).
.PHONY: navdata
navdata:
	scripts/fetch-navdata.sh
