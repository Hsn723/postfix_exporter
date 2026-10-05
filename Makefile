LDFLAGS = -ldflags "-s -w"
BINDIR = $(shell pwd)/bin
YQ = $(BINDIR)/yq
SUDO = $(shell if [ "$$(id -u)" -ne 0 ]; then echo "sudo"; fi)

.PHONY: libsystemd-dev
libsystemd-dev:
	@dpkg -s $@ >/dev/null 2>&1 || $(SUDO) apt-get update && $(SUDO) apt-get install -y --no-install-recommends $@

.PHONY: gcc-aarch64-linux-gnu
gcc-aarch64-linux-gnu:
	@dpkg -s $@ >/dev/null 2>&1 || $(SUDO) apt-get update && $(SUDO) apt-get install -y $@

.PHONY: test
test: libsystemd-dev
	go test -coverprofile cover.out -count=1 -race -p 4 -v ./...

.PHONY: lint
lint: libsystemd-dev
	if [ -z "$(shell which pre-commit)" ]; then pip3 install pre-commit; fi
	pre-commit install
	pre-commit run --all-files

.PHONY: build
build: libsystemd-dev
	go build $(LDFLAGS) .

$(BINDIR):
	mkdir -p $(BINDIR)

CONTAINER_STRUCTURE_TEST = $(BINDIR)/container-structure-test
.PHONY: $(CONTAINER_STRUCTURE_TEST)
$(CONTAINER_STRUCTURE_TEST): $(BINDIR)
	curl -sSLf -o $(CONTAINER_STRUCTURE_TEST) https://github.com/GoogleContainerTools/container-structure-test/releases/latest/download/container-structure-test-linux-amd64 && chmod +x $(CONTAINER_STRUCTURE_TEST)

.PHONY: container-structure-test
container-structure-test: $(CONTAINER_STRUCTURE_TEST) $(YQ)
	$(YQ) '.builds[] | select(.id == "default") | .goarch[]' .goreleaser.yml | xargs -I {} $(CONTAINER_STRUCTURE_TEST) test --image ghcr.io/hsn723/postfix_exporter:$(shell git describe --tags --abbrev=0 --match "v*" || echo v0.0.0)-next-{} --platform linux/{} --config cst.yaml

.PHONY: $(YQ)
$(YQ): $(BINDIR)
	GOBIN=$(BINDIR) go install github.com/mikefarah/yq/v4@latest
