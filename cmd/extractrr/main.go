package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/spf13/cobra"
)

/*
#cgo pkg-config: udfread
#include <stdlib.h>
#include <udfread/udfread.h>
*/
import "C"
import "unsafe"

// Job represents a file extraction task
type Job struct {
	SrcPath string
	DstPath string
	Size    int64
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "extractrr",
		Short: "extract iso to directory",
		Long: `Extract iso to directory

Documentation is available at https://github.com/autobrr/extractrr`,
	}

	rootCmd.AddCommand(CommandExtract())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func CommandExtract() *cobra.Command {
	var command = &cobra.Command{
		Use:     "extract",
		Short:   "Extract iso to directory",
		Example: `  extractrr extract /path/to/file.iso /path/to/export`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 2 {
				return fmt.Errorf("requires two args")
			}
			return nil
		},
	}

	var (
		numWorkers   = command.Flags().Int("workers", runtime.NumCPU(), "Number of parallel workers")
		bufferSize   = command.Flags().Int("buffer", 1024*1024, "Buffer size for file copying (bytes)")
		showProgress = command.Flags().Bool("progress", true, "Show progress bar")
	)

	command.RunE = func(c *cobra.Command, args []string) error {
		isoFile := args[0]
		extractDir := args[1]

		startTime := time.Now()

		// Ensure extract directory exists
		if err := os.MkdirAll(extractDir, 0755); err != nil {
			return fmt.Errorf("failed to create extract directory: %w", err)
		}

		log.Printf("Initializing UDF reader for %s...", isoFile)
		// Open UDF filesystem
		cIsoPath := C.CString(isoFile)
		defer C.free(unsafe.Pointer(cIsoPath))

		udf := C.udfread_init()
		if udf == nil {
			return fmt.Errorf("failed to initialize UDF reader")
		}
		defer C.udfread_close(udf)

		if C.udfread_open(udf, cIsoPath) != 0 {
			return fmt.Errorf("failed to open ISO file: %s", isoFile)
		}

		// First pass: scan the ISO structure to gather file info
		// This helps with showing progress and planning extraction
		log.Printf("Scanning ISO structure...")
		var totalSize int64
		var fileCount int
		jobs := make([]Job, 0)

		err := scanISOStructure(udf, "/", extractDir, &jobs, &totalSize, &fileCount)
		if err != nil {
			return fmt.Errorf("failed to scan ISO: %w", err)
		}

		log.Printf("Found %d files with total size of %.2f GB", fileCount, float64(totalSize)/(1024*1024*1024))

		// Create worker pool and job channel
		jobChan := make(chan Job, fileCount)
		var wg sync.WaitGroup

		// Setup progress bar if enabled
		var bar *pb.ProgressBar
		if *showProgress {
			bar = pb.Full.Start64(totalSize)
			defer bar.Finish()
		}

		// Progress tracking
		progressChan := make(chan int64)
		go func() {
			var processedSize int64
			for size := range progressChan {
				processedSize += size
				if bar != nil {
					bar.SetCurrent(processedSize)
				}
			}
		}()

		// Start worker goroutines
		for i := 0; i < *numWorkers; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				// Each worker gets its own UDF handle to avoid concurrency issues
				workerUdf := C.udfread_init()
				if workerUdf == nil {
					log.Printf("Worker %d: Failed to initialize UDF reader", id)
					return
				}
				defer C.udfread_close(workerUdf)

				cWorkerIsoPath := C.CString(isoFile)
				defer C.free(unsafe.Pointer(cWorkerIsoPath))

				if C.udfread_open(workerUdf, cWorkerIsoPath) != 0 {
					log.Printf("Worker %d: Failed to open ISO file", id)
					return
				}

				buffer := make([]byte, *bufferSize)

				for job := range jobChan {
					err := extractFile(workerUdf, job.SrcPath, job.DstPath, buffer, progressChan)
					if err != nil {
						log.Printf("Error extracting %s: %v", job.SrcPath, err)
					}
				}
			}(i)
		}

		// Submit jobs to the pool
		log.Printf("Starting extraction with %d workers...", *numWorkers)
		for _, job := range jobs {
			jobChan <- job
		}
		close(jobChan)

		// Wait for all workers to complete
		wg.Wait()
		close(progressChan)

		duration := time.Since(startTime)
		log.Printf("Extraction completed in %v", duration)
		if totalSize > 0 {
			speedMBps := float64(totalSize) / (1024 * 1024) / duration.Seconds()
			log.Printf("Average speed: %.2f MB/s", speedMBps)
		}

		return nil
	}

	return command
}

// scanISOStructure recursively scans the ISO structure and builds a list of files to extract
func scanISOStructure(udf *C.udfread, path, destPath string, jobs *[]Job, totalSize *int64, fileCount *int) error {
	// Create the destination directory
	if err := os.MkdirAll(destPath, 0755); err != nil {
		return err
	}

	// Convert path to C string
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	// Open directory
	dir := C.udfread_opendir(udf, cPath)
	if dir == nil {
		return fmt.Errorf("failed to open directory: %s", path)
	}
	defer C.udfread_closedir(dir)

	// Read directory entries
	for {
		var dirent C.struct_udfread_dirent
		result := C.udfread_readdir(dir, &dirent)
		if result == nil {
			break
		}

		// Convert entry name to Go string
		name := C.GoString(dirent.d_name)

		// Skip "." and ".."
		if name == "." || name == ".." {
			continue
		}

		// Create full paths
		srcPath := filepath.Join(path, name)
		fileDestPath := filepath.Join(destPath, name)

		// Handle based on entry type
		if dirent.d_type == C.UDF_DT_DIR {
			// Recursively scan subdirectory
			if err := scanISOStructure(udf, srcPath, fileDestPath, jobs, totalSize, fileCount); err != nil {
				return err
			}
		} else if dirent.d_type == C.UDF_DT_REG {
			// Get file size
			size, err := getFileSize(udf, srcPath)
			if err != nil {
				return err
			}

			*jobs = append(*jobs, Job{
				SrcPath: srcPath,
				DstPath: fileDestPath,
				Size:    size,
			})

			*totalSize += size
			*fileCount++
		}
	}

	return nil
}

// getFileSize returns the size of a file
func getFileSize(udf *C.udfread, path string) (int64, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	file := C.udfread_file_open(udf, cPath)
	if file == nil {
		return 0, fmt.Errorf("failed to open file: %s", path)
	}
	defer C.udfread_file_close(file)

	size := C.udfread_file_size(file)
	if size < 0 {
		return 0, fmt.Errorf("failed to get file size: %s", path)
	}

	return int64(size), nil
}

// extractFile extracts a single file using the provided buffer
func extractFile(udf *C.udfread, srcPath, destPath string, buffer []byte, progressChan chan<- int64) error {
	// Convert source path to C string
	cSrcPath := C.CString(srcPath)
	defer C.free(unsafe.Pointer(cSrcPath))

	// Create parent directories if needed
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	// Open source file
	file := C.udfread_file_open(udf, cSrcPath)
	if file == nil {
		return fmt.Errorf("failed to open file: %s", srcPath)
	}
	defer C.udfread_file_close(file)

	// Create destination file
	destFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	// Copy file contents in chunks using the provided buffer
	for {
		bytesRead := C.udfread_file_read(file, unsafe.Pointer(&buffer[0]), C.size_t(len(buffer)))
		if bytesRead <= 0 {
			break
		}

		n, err := destFile.Write(buffer[:bytesRead])
		if err != nil {
			return err
		}

		// Report progress
		progressChan <- int64(n)
	}

	return nil
}
