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

RUN go build -a -tags netgo -ldflags '-w -extldflags "-static"' -o bin/extractrr ./cmd/extractrr/main.go

FROM scratch AS export-stage
COPY --from=build-stage /src/bin/ .
