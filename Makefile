GO = go
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD)
BUILD_TIME ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

build-docker:
	DOCKER_BUILDKIT=1 docker build \
		--file Dockerfile \
		--output out \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILDTIME=$(BUILD_TIME) \
		.

build-bin:
	go build -a -tags netgo -ldflags "-w -extldflags \"-static\" -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_TIME}" -o bin/extractrr ./cmd/extractrr/main.go
