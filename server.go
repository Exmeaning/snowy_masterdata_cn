package main

import (
	"context"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Server 内嵌 HTTP 文件服务器，支持按需压缩
type Server struct {
	serveDir   string
	compressor *Compressor
	port       string
}

func NewServer(serveDir string, compressor *Compressor, port string) *Server {
	return &Server{
		serveDir:   serveDir,
		compressor: compressor,
		port:       port,
	}
}

// Run 启动 HTTP 服务，阻塞直到 ctx 取消
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", s.handleFile)

	srv := &http.Server{
		Addr:    ":" + s.port,
		Handler: mux,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("HTTP server listening on :%s (serve-dir=%s)", s.port, s.serveDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	// OPTIONS 预检
	if r.Method == http.MethodOptions {
		setCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// 清理路径，防止目录遍历
	urlPath := filepath.Clean(r.URL.Path)
	if urlPath == "/" || urlPath == "." {
		http.NotFound(w, r)
		return
	}
	filePath := filepath.Join(s.serveDir, filepath.FromSlash(urlPath))

	// 安全检查：不允许访问 serve 目录之外
	if !strings.HasPrefix(filePath, s.serveDir) {
		http.NotFound(w, r)
		return
	}

	// 不允许直接请求 .gz / .br 文件
	if strings.HasSuffix(filePath, ".gz") || strings.HasSuffix(filePath, ".br") {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	setCORS(w)
	setCacheControl(w, urlPath)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// 尝试提供预压缩文件
	if s.tryPrecompressed(w, r, filePath, info.ModTime()) {
		return
	}

	// 没有预压缩文件 → 返回源文件，后台触发异步压缩
	if isCompressible(filePath) && info.Size() >= minCompressSize {
		s.compressor.CompressFileAsync(filePath)
	}

	serveOriginal(w, r, filePath, info.ModTime())
}

// tryPrecompressed 检查并提供预压缩文件，成功返回 true
func (s *Server) tryPrecompressed(w http.ResponseWriter, r *http.Request, filePath string, modTime time.Time) bool {
	ae := r.Header.Get("Accept-Encoding")

	// 优先 Brotli
	if strings.Contains(ae, "br") {
		brPath := filePath + ".br"
		if brInfo, err := os.Stat(brPath); err == nil && brInfo.ModTime().After(modTime) {
			w.Header().Set("Content-Encoding", "br")
			setContentType(w, filePath)
			http.ServeFile(w, r, brPath)
			return true
		}
	}

	// 其次 Gzip
	if strings.Contains(ae, "gzip") {
		gzPath := filePath + ".gz"
		if gzInfo, err := os.Stat(gzPath); err == nil && gzInfo.ModTime().After(modTime) {
			w.Header().Set("Content-Encoding", "gzip")
			setContentType(w, filePath)
			http.ServeFile(w, r, gzPath)
			return true
		}
	}

	return false
}

func serveOriginal(w http.ResponseWriter, r *http.Request, filePath string, modTime time.Time) {
	f, err := os.Open(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, filepath.Base(filePath), modTime, f)
}

func setContentType(w http.ResponseWriter, originalPath string) {
	ext := filepath.Ext(originalPath)
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
}

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept-Encoding")
}

func setCacheControl(w http.ResponseWriter, urlPath string) {
	if strings.HasSuffix(urlPath, ".json") {
		w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	}
}
