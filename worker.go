package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func EnsureDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}

func moveFile(sourcePath, destDir string) error {
	err := EnsureDir(destDir)
	if err != nil {
		return err
	}

	fileName := filepath.Base(sourcePath)
	// if it ends with .sql.processing, remove the .processing part when moving to done/error
	if strings.HasSuffix(fileName, ".sql.processing") {
		fileName = strings.TrimSuffix(fileName, ".processing")
	}

	destPath := filepath.Join(destDir, fileName)

	err = os.Rename(sourcePath, destPath)
	if err != nil {
		err = copyFile(sourcePath, destPath)
		if err == nil {
			os.Remove(sourcePath)
		}
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Close()
}

func worker(ctx context.Context, id int, cfg *Config, rc *RabbitClient, jobs <-chan string, wg *sync.WaitGroup) {
	defer wg.Done()
	log.Printf("Worker %d started\n", id)

	for {
		select {
		case filePath, ok := <-jobs:
			if !ok {
				log.Printf("Worker %d stopping (jobs channel closed)\n", id)
				return
			}
			processFile(ctx, id, cfg, rc, filePath)
		case <-ctx.Done():
			log.Printf("Worker %d stopping (context done)\n", id)
			return
		}
	}
}

func processFile(ctx context.Context, workerId int, cfg *Config, rc *RabbitClient, filePath string) {
	// If it's a new .sql file, wait 200ms and rename it
	processingPath := filePath
	if strings.HasSuffix(filePath, ".sql") {
		log.Printf("[Worker %d] Waiting 200ms for file: %s to settle\n", workerId, filePath)
		time.Sleep(200 * time.Millisecond)

		processingPath = filePath + ".processing"
		err := os.Rename(filePath, processingPath)
		if err != nil {
			log.Printf("[Worker %d] Failed to lock/rename file %s: %v. Will try later.\n", workerId, filePath, err)
			return // Cannot lock, might be in use by the OS. Scanner will pick it up again.
		}
	}

	log.Printf("[Worker %d] Processing file: %s\n", workerId, processingPath)

	content, err := os.ReadFile(processingPath)
	if err != nil {
		log.Printf("[Worker %d] Failed to read file %s: %v\n", workerId, processingPath, err)
		// Try to revert rename to let another try later
		os.Rename(processingPath, strings.TrimSuffix(processingPath, ".processing"))
		return
	}

retryLoop:
	for {
		err = rc.ExecuteSQLRPC(ctx, cfg.TaskQueue, string(content), cfg.RPCTimeoutSeconds)
		if err == nil {
			break retryLoop // Success
		}
		
		log.Printf("[Worker %d] RPC Failed for %s: %v. Retrying in 5 seconds...\n", workerId, processingPath, err)
		
		select {
		case <-time.After(5 * time.Second):
			// continue loop
		case <-ctx.Done():
			log.Printf("[Worker %d] Context canceled during retry for %s, returning to .sql...\n", workerId, processingPath)
			// Return to .sql so we don't lose the processing state
			os.Rename(processingPath, strings.TrimSuffix(processingPath, ".processing"))
			return
		}
	}

	log.Printf("[Worker %d] Successfully executed SQL for %s\n", workerId, processingPath)
	errMove := moveFile(processingPath, cfg.DoneDir)
	if errMove != nil {
		log.Printf("[Worker %d] Failed to move file to done dir %s: %v\n", workerId, processingPath, errMove)
	}
}

func RunScannerAndWorkers(ctx context.Context, cfg *Config, rc *RabbitClient) {
	jobs := make(chan string, 100)
	var wg sync.WaitGroup

	for i := 1; i <= cfg.WorkerCount; i++ {
		wg.Add(1)
		go worker(ctx, i, cfg, rc, jobs, &wg)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	inProgress := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			log.Println("Scanner and Workers gracefully stopped")
			return
		case <-ticker.C:
			entries, err := os.ReadDir(cfg.SourceDir)
			if err != nil {
				log.Printf("Failed to scan directory %s: %v\n", cfg.SourceDir, err)
				continue
			}

			currentFiles := make(map[string]bool)
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := strings.ToLower(entry.Name())
				// Pick both .sql and .sql.processing
				if strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".sql.processing") {
					path := filepath.Join(cfg.SourceDir, entry.Name())
					currentFiles[path] = true

					if !inProgress[path] {
						inProgress[path] = true
						select {
						case jobs <- path:
						default:
							inProgress[path] = false
							break
						}
					}
				}
			}

			for path := range inProgress {
				if !currentFiles[path] {
					delete(inProgress, path)
				}
			}
		}
	}
}
