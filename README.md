# extractrr

A tool to extract iso files to disk without mounting.

## Build

go build -o iso-extract main.go

## Usage

### Basic usage
    ./extractrr /path/to/large.iso /path/to/extract

### Tuned for HDD
    ./extractrr /path/to/large.iso /path/to/extract --buffer 4194304 --workers 4

### Tuned for high-speed SSD/NVMe
    ./extract /path/to/large.iso /path/to/extract --buffer 524288 --workers 16

### Disable progress bar for log files
    ./extractrr /path/to/large.iso /path/to/extract --progress=false