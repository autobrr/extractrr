FROM golang:1.24-bookworm AS build-stage

# Install build dependencies
RUN apt-get update && apt-get install -y \
    build-essential \
    automake \
    autoconf \
    libtool \
    pkg-config \
    git

# Build libudfread from source with static library
WORKDIR /tmp
RUN git clone https://code.videolan.org/videolan/libudfread.git && \
    cd libudfread && \
    ./bootstrap && \
    ./configure --prefix=/usr --enable-static --disable-shared && \
    make && \
    make install

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . ./

ARG VERSION=dev
ARG COMMIT=dev
ARG BUILDTIME

# Use these args in your build
RUN echo "Building version ${VERSION} commit ${COMMIT} at ${BUILDTIME}"

RUN go build -a -tags netgo -ldflags "-w -extldflags \"-static\" -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILDTIME}" -o bin/extractrr ./cmd/extractrr/main.go

FROM scratch AS export-stage
COPY --from=build-stage /src/bin/ .
