package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"github.com/cheggaaa/pb/v3"
	"github.com/creativeprojects/go-selfupdate"
	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

/*
#cgo pkg-config: libudfread
#include <stdlib.h>
#include <udfread/udfread.h>
*/
import "C"
import "unsafe"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

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
	rootCmd.AddCommand(CommandVersion())
	rootCmd.AddCommand(CommandUpdate())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func CommandVersion() *cobra.Command {
	var command = &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(c *cobra.Command, args []string) {
			fmt.Printf("extractrr %s (%s, %s)\n", version, commit, date)
		},
	}

	return command
}

func CommandUpdate() *cobra.Command {
	var command = &cobra.Command{
		Use:   "update",
		Short: "Update extractrr to the latest version",
		Long:  "Update extractrr to the latest version from GitHub releases",
		RunE: func(cmd *cobra.Command, args []string) error {
			// If version is in dev mode, skip update
			if version == "dev" {
				fmt.Println("Cannot update development version")
				return nil
			}

			fmt.Printf("Current version: %s\n", version)
			fmt.Println("Checking for updates...")

			// Parse current version with semver for comparison
			_, err := semver.ParseTolerant(version)
			if err != nil {
				return fmt.Errorf("could not parse version: %w", err)
			}

			latest, found, err := selfupdate.DetectLatest(cmd.Context(), selfupdate.ParseSlug("autobrr/extractrr"))
			if err != nil {
				return fmt.Errorf("error occurred while detecting version: %w", err)
			}
			if !found {
				return fmt.Errorf("latest version for %s/%s could not be found from github repository", "autobrr/extractrr", version)
			}

			if latest.LessOrEqual(version) {
				fmt.Printf("Current binary is the latest version: %s\n", version)
				return nil
			}

			exe, err := selfupdate.ExecutablePath()
			if err != nil {
				return fmt.Errorf("could not locate executable path: %w", err)
			}

			if err := selfupdate.UpdateTo(cmd.Context(), latest.AssetURL, latest.AssetName, exe); err != nil {
				return fmt.Errorf("error occurred while updating binary: %w", err)
			}

			fmt.Printf("Successfully updated to version: %s\n", latest.Version())

			return nil
		},
	}

	return command
}

func CommandExtract() *cobra.Command {
	var command = &cobra.Command{
		Use:   "extract",
		Short: "Extract iso to directory",
		Example: `  extractrr extract /path/to/file.iso /path/to/export
  extractrr extract "/path/to/*.iso" /path/to/export`,
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
		pattern := args[0]
		extractBaseDir := args[1]

		// Expand the glob pattern to get all matching files
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("invalid glob pattern: %w", err)
		}

		if len(matches) == 0 {
			return fmt.Errorf("no files found matching pattern: %s", pattern)
		}

		// If only one file matches, use the exact extractDir provided
		if len(matches) == 1 {
			return extractISO(matches[0], extractBaseDir, *numWorkers, *bufferSize, *showProgress)
		}

		// Multiple files matched the pattern
		log.Printf("Found %d files matching the pattern", len(matches))

		// Process each file in sequence
		for _, isoFile := range matches {
			// For multiple files, create subdirectories based on filename
			baseName := filepath.Base(isoFile)
			fileNameWithoutExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
			fileExtractDir := filepath.Join(extractBaseDir, fileNameWithoutExt)

			log.Printf("Processing %s -> %s", isoFile, fileExtractDir)
			if err := extractISO(isoFile, fileExtractDir, *numWorkers, *bufferSize, *showProgress); err != nil {
				// Log error but continue with next file
				log.Printf("Error extracting %s: %v", isoFile, err)
			}
		}

		return nil
	}

	return command
}

// extractISO handles the extraction of a single ISO file to a target directory
func extractISO(isoFile, extractDir string, numWorkers int, bufferSize int, showProgress bool) error {
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

	log.Printf("Found %d files with total size of %s", fileCount, humanize.IBytes(uint64(totalSize)))

	// Create worker pool and job channel
	jobChan := make(chan Job, fileCount)
	var wg sync.WaitGroup

	// Setup progress bar if enabled
	var bar *pb.ProgressBar
	if showProgress {
		bar = pb.Full.Start64(totalSize)
		bar.Set(pb.Bytes, true)
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
	for i := 0; i < numWorkers; i++ {
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

			buffer := make([]byte, bufferSize)

			for job := range jobChan {
				err := extractFile(workerUdf, job.SrcPath, job.DstPath, buffer, progressChan)
				if err != nil {
					log.Printf("Error extracting %s: %v", job.SrcPath, err)
				}
			}
		}(i)
	}

	// Submit jobs to the pool
	log.Printf("Starting extraction with %d workers...", numWorkers)
	for _, job := range jobs {
		jobChan <- job
	}
	close(jobChan)

	// Wait for all workers to complete
	wg.Wait()
	close(progressChan)

	if bar != nil {
		bar.SetCurrent(totalSize)
		bar.Finish()
	}

	duration := time.Since(startTime)

	log.Printf("Extraction completed in %v", duration)
	if totalSize > 0 && duration.Seconds() > 0 {
		speedBytesPerSec := float64(totalSize) / duration.Seconds()
		log.Printf("Average speed: %s/s", humanize.IBytes(uint64(speedBytesPerSec)))
	} else if totalSize > 0 {
		log.Printf("Average speed: N/A (extraction too fast)")
	}

	return nil
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
