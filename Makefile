SHELL=/bin/bash -o pipefail

GO_PKG              := go.openviz.dev
REPO                := $(notdir $(shell pwd))
BIN                 := alertmanager-relay
IMAGE_NAME          ?= $(BIN)
DOCKER_REGISTRY     ?= $(REGISTRY)
VERSION             ?= latest
FULL_IMAGE_NAME     := $(DOCKER_REGISTRY)/$(IMAGE_NAME):$(VERSION)

GO_VERSION          ?= 1.26
BUILD_IMAGE         ?= ghcr.io/appscode/golang-dev:$(GO_VERSION)

OS                  := $(if $(GOOS),$(GOOS),$(shell go env GOOS))
ARCH                := $(if $(GOARCH),$(GOARCH),$(shell go env GOARCH))
SRC_DIRS            := cmd internal
BUILD_DIRS          := .go/bin/$(OS)_$(ARCH) .go/cache
DOCKER_REPO_ROOT    := /go/src/$(GO_PKG)/$(REPO)

K8S_NAMESPACE            ?= monitoring
WEBHOOK_URLS_SECRET_NAME ?= alertmanager-relay-webhook-urls
LISTEN_ADDR              ?= :8080
REQUEST_TIMEOUT          ?= 5s
SEND_RESOLVED            ?= true

.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build       - Build local binary"
	@echo "  push        - Build and push Docker image"
	@echo "  deploy      - Deploy to Kubernetes"
	@echo "  clean       - Remove Kubernetes resources"
	@echo "  test        - Run tests"
	@echo "  lint        - Run linter"
	@echo "  fmt         - Format Go files"
	@echo "  verify-fmt  - Verify formatting"

.PHONY: build
build:
	go build ./cmd/alertmanager-relay

.PHONY: test
test:
	go test ./...

.PHONY: fmt
fmt:
	gofmt -w $(SRC_DIRS)

.PHONY: verify-fmt
verify-fmt:
	@gofmt -w $(SRC_DIRS)
	@git diff --exit-code

.PHONY: lint
lint: $(BUILD_DIRS)
	@echo "running linter"
	@docker run                                                 \
	    -i                                                      \
	    --rm                                                    \
	    -u $$(id -u):$$(id -g)                                  \
	    -v $$(pwd):/src                                         \
	    -w /src                                                 \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin                \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin/$(OS)_$(ARCH)  \
	    -v $$(pwd)/.go/cache:/.cache                            \
	    --env HTTP_PROXY=$(HTTP_PROXY)                          \
	    --env HTTPS_PROXY=$(HTTPS_PROXY)                        \
	    $(BUILD_IMAGE)                                          \
	    golangci-lint run

.PHONY: container
container:
ifeq ($(DOCKER_REGISTRY),)
	$(error DOCKER_REGISTRY must be set via DOCKER_REGISTRY or REGISTRY)
endif
	docker build -t $(FULL_IMAGE_NAME) .

.PHONY: push
push: container
	docker push $(FULL_IMAGE_NAME)

.PHONY: clean
clean:
	kubectl delete deployment $(BIN) -n $(K8S_NAMESPACE) --ignore-not-found=true || true
	kubectl delete service $(BIN) -n $(K8S_NAMESPACE) --ignore-not-found=true || true

.PHONY: deploy
deploy: clean push
	kubectl create namespace $(K8S_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
ifneq ($(strip $(WEBHOOK_URLS)),)
	kubectl create secret generic $(WEBHOOK_URLS_SECRET_NAME) \
		--namespace $(K8S_NAMESPACE) \
		--from-literal=url='$(WEBHOOK_URLS)' \
		--dry-run=client -o yaml | kubectl apply -f -
endif
	@echo "Deploying $(FULL_IMAGE_NAME) to namespace $(K8S_NAMESPACE)..."
		sed -e "s|\$${DOCKER_REGISTRY}|$(DOCKER_REGISTRY)|g" \
		-e "s|\$${IMAGE_NAME}|$(IMAGE_NAME)|g" \
		-e "s|\$${VERSION}|$(VERSION)|g" \
		-e "s|\$${LISTEN_ADDR}|$(LISTEN_ADDR)|g" \
		-e "s|\$${REQUEST_TIMEOUT}|$(REQUEST_TIMEOUT)|g" \
		-e "s|\$${SEND_RESOLVED}|$(SEND_RESOLVED)|g" \
		-e "s|\$${WEBHOOK_URLS_SECRET_NAME}|$(WEBHOOK_URLS_SECRET_NAME)|g" \
		k8s/deployment.yaml | kubectl apply -n $(K8S_NAMESPACE) -f -
	kubectl apply -n $(K8S_NAMESPACE) -f k8s/service.yaml

.PHONY: ci
ci: verify-fmt lint test

$(BUILD_DIRS):
	@mkdir -p $@
