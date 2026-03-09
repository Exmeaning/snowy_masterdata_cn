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
	"sync"
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
	workers    int
	pending    sync.Map // 按需压缩去重：path → struct{}
	asyncPool  *WorkerPool
	asyncReady chan struct{} // 延迟初始化信号
}

func NewCompressor(workers int) *Compressor {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	return &Compressor{
		workers:    workers,
		asyncReady: make(chan struct{}),
	}
}

// InitAsyncPool 初始化按需压缩的后台工作池（serve 模式调用）
func (c *Compressor) InitAsyncPool() {
	poolSize := c.workers / 2
	if poolSize < 1 {
		poolSize = 1
	}
	c.asyncPool = NewWorkerPool(poolSize)
	close(c.asyncReady)
	log.Printf("Async compression pool ready (workers=%d)", poolSize)
}

// CompressFileAsync 按需异步压缩单个文件（去重，不阻塞调用方）
func (c *Compressor) CompressFileAsync(path string) {
	// 去重：同一文件只触发一次
	if _, loaded := c.pending.LoadOrStore(path, struct{}{}); loaded {
		return
	}

	// 等待 pool 就绪（正常流程下 InitAsyncPool 已调用，不会阻塞）
	<-c.asyncReady

	c.asyncPool.Submit(func() {
		defer c.pending.Delete(path)

		info, err := os.Stat(path)
		if err != nil || !isCompressible(path) || info.Size() < minCompressSize {
			return
		}
		if err := compressFile(path); err != nil {
			log.Printf("WARNING: async compress %s: %v", path, err)
			return
		}
		log.Printf("Lazy compressed: %s", path)
	})
}

// InvalidateCompressed 清理文件的预压缩版本（文件变更时调用，让下次请求重新触发按需压缩）
func (c *Compressor) InvalidateCompressed(servePath string) {
	os.Remove(servePath + ".gz")
	os.Remove(servePath + ".br")
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
