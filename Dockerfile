FROM golang:1.24-bookworm AS build-stage
#RUN apt-get update && apt-get install libudfread0 libudfread-dev pkg-config -y
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN dpkg -i deps/*.deb

RUN go build -a -tags netgo -ldflags '-w -extldflags "-static"' -o bin/extractrr ./cmd/extractrr/main.go

FROM scratch AS export-stage
COPY --from=build-stage /src/bin/ .
