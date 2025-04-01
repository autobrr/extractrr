# extractrr

A tool to extract iso files to disk without mounting.

## Build

The easiest way to build this is with docker using the Dockerfile and either call `make build-docker` or `./build.sh`.

It includes an extra debian package `libudfread` which is copied into the container during build.

## Usage

### Basic usage
    ./extractrr /path/to/large.iso /path/to/extract

### Tuned for HDD
    ./extractrr /path/to/large.iso /path/to/extract --buffer 4194304 --workers 4

### Tuned for high-speed SSD/NVMe
    ./extract /path/to/large.iso /path/to/extract --buffer 524288 --workers 16

### Disable progress bar for log files
    ./extractrr /path/to/large.iso /path/to/extract --progress=false