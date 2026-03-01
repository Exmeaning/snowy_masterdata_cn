package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/pgzip"
)

var compressibleExtensions = map[string]bool{
	".json": true, ".js": true, ".css": true, ".html": true,
	".htm": true, ".xml": true, ".svg": true, ".txt": true,
	".csv": true, ".md": true, ".yaml": true, ".yml": true,
	".toml": true, ".ini": true, ".cfg": true, ".conf": true,
	".map": true, ".wasm": true, ".ico": true,
	".ttf": true, ".otf": true, ".eot": true,
	".woff": true, ".woff2": true,
}

const minCompressSize = 256

type Compressor struct {
	workers int
}

func NewCompressor(workers int) *Compressor {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	return &Compressor{workers: workers}
}

func isCompressible(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return compressibleExtensions[ext]
}

// CompressAll 全量压缩（仅在构建阶段调用）
func (c *Compressor) CompressAll(ctx context.Context, dir string) error {
	start := time.Now()
	var totalFiles, compressedFiles, totalOrigSize int64

	pool := NewWorkerPool(c.workers)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".gz") || strings.HasSuffix(path, ".br") {
			return nil
		}

		atomic.AddInt64(&totalFiles, 1)

		if !isCompressible(path) || info.Size() < minCompressSize {
			return nil
		}

		size := info.Size()
		filePath := path
		pool.Submit(func() {
			if err := compressFile(filePath); err != nil {
				log.Printf("WARNING: compress %s: %v", filePath, err)
				return
			}
			atomic.AddInt64(&compressedFiles, 1)
			atomic.AddInt64(&totalOrigSize, size)
		})
		return nil
	})

	pool.Wait()

	if err != nil {
		return err
	}

	log.Printf("Compression done: %d/%d files, %.1f MB original, %v elapsed",
		compressedFiles, totalFiles, float64(totalOrigSize)/1024/1024, time.Since(start))
	return nil
}

// CompressFiles 增量压缩（生产阶段调用）
func (c *Compressor) CompressFiles(ctx context.Context, files []string) error {
	pool := NewWorkerPool(c.workers)

	for _, f := range files {
		file := f
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pool.Submit(func() {
			info, err := os.Stat(file)
			if err != nil {
				// 文件被删除，清理压缩文件
				os.Remove(file + ".gz")
				os.Remove(file + ".br")
				return
			}
			if !isCompressible(file) || info.Size() < minCompressSize {
				os.Remove(file + ".gz")
				os.Remove(file + ".br")
				return
			}
			if err := compressFile(file); err != nil {
				log.Printf("WARNING: compress %s: %v", file, err)
			}
		})
	}

	pool.Wait()
	return nil
}

// RemoveCompressed 删除文件及其压缩版本
func (c *Compressor) RemoveCompressed(servePath string) {
	os.Remove(servePath + ".gz")
	os.Remove(servePath + ".br")
	os.Remove(servePath)
}

func compressFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	// Gzip
	if err := writeGzip(path+".gz", data); err != nil {
		return fmt.Errorf("gzip: %w", err)
	}

	// Brotli
	if err := writeBrotli(path+".br", data); err != nil {
		return fmt.Errorf("brotli: %w", err)
	}

	return nil
}

func writeGzip(out string, data []byte) error {
	var buf bytes.Buffer
	w, err := pgzip.NewWriterLevel(&buf, pgzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0644)
}

func writeBrotli(out string, data []byte) error {
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.WriteFile(out, buf.Bytes(), 0644)
}
