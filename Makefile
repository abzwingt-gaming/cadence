# Makefile — local dev builds.
# REGISTRY and VERSION can be overridden: make build-all REGISTRY=myorg VERSION=1.2.3
REGISTRY ?= ghcr.io/abzwingt-gaming
VERSION  ?= dev

build-init:
	docker buildx create --platform linux/arm64,linux/amd64 --use --name multiarch

build-cadence:
	docker buildx build --push \
	  --platform linux/arm64,linux/amd64 \
	  --tag $(REGISTRY)/cadence:latest \
	  --tag $(REGISTRY)/cadence:$(VERSION) \
	  --file ./src/cadence.Dockerfile ./src/

build-icecast2:
	docker buildx build --push \
	  --platform linux/arm64,linux/amd64 \
	  --tag $(REGISTRY)/cadence-icecast2:latest \
	  --tag $(REGISTRY)/cadence-icecast2:$(VERSION) \
	  --file ./src/icecast2.Dockerfile ./src/

build-liquidsoap:
	docker buildx build --push \
	  --platform linux/arm64,linux/amd64 \
	  --tag $(REGISTRY)/cadence-liquidsoap:latest \
	  --tag $(REGISTRY)/cadence-liquidsoap:$(VERSION) \
	  --file ./src/liquidsoap.Dockerfile ./src/

build-all: build-cadence build-icecast2 build-liquidsoap

.PHONY: build-init build-cadence build-icecast2 build-liquidsoap build-all
