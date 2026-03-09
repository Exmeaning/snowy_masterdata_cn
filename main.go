package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

const (
	repoURL      = "https://github.com/Team-Haruki/haruki-sekai-sc-master.git"
	pollInterval = 1 * time.Minute
)

var (
	flagMode     string
	flagRepoDir  string
	flagServeDir string
	flagWorkers  int
	flagPort     string
)

func init() {
	flag.StringVar(&flagMode, "mode", "serve", "运行模式: build (构建阶段全量压缩) | serve (生产阶段增量监测)")
	flag.StringVar(&flagRepoDir, "repo", "/data/repo", "Git 仓库目录")
	flag.StringVar(&flagServeDir, "serve-dir", "/data/serve", "静态文件服务目录")
	flag.IntVar(&flagWorkers, "workers", 0, "并行压缩线程数 (0=CPU核心数)")
	flag.StringVar(&flagPort, "port", "", "HTTP 监听端口 (默认读取 PORT 环境变量，兜底 80)")
}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if flagWorkers <= 0 {
		flagWorkers = runtime.NumCPU()
	}

	log.Printf("=== Haruki Builder [mode=%s, cpus=%d, workers=%d] ===", flagMode, runtime.NumCPU(), flagWorkers)

	switch flagMode {
	case "build":
		runBuild()
	case "serve":
		runServe()
	default:
		log.Fatalf("Unknown mode: %s (use 'build' or 'serve')", flagMode)
	}
}

// ══════════════════════════════════════════════════════════════
// build 模式：Docker 构建阶段执行，全量 clone + 预处理 + 压缩
// ══════════════════════════════════════════════════════════════
func runBuild() {
	ctx := context.Background()

	// 1. Clone
	if err := fullClone(ctx, flagRepoDir); err != nil {
		log.Fatalf("Clone failed: %v", err)
	}

	// 2. 预处理
	log.Println("Running preprocessor...")
	if err := RunPreprocessor(flagRepoDir); err != nil {
		log.Printf("WARNING: Preprocessor failed: %v", err)
	}
	if err := RunMoePreprocessor(flagRepoDir); err != nil {
		log.Printf("WARNING: MoePreprocessor failed: %v", err)
	}

	// 3. 同步到 serve 目录（排除 .git）
	if err := syncToServeDir(flagRepoDir, flagServeDir); err != nil {
		log.Fatalf("Sync failed: %v", err)
	}

	// 4. 记录当前 commit hash 供运行阶段使用
	hash, err := getCurrentCommit(ctx, flagRepoDir)
	if err != nil {
		log.Fatalf("Failed to get commit hash: %v", err)
	}
	commitFile := filepath.Join(flagServeDir, ".last_commit")
	if err := os.WriteFile(commitFile, []byte(hash), 0644); err != nil {
		log.Fatalf("Failed to write commit hash: %v", err)
	}
	log.Printf("Current commit: %s", hash)

	// 5. 全量压缩
	log.Println("Starting full pre-compression...")
	compressor := NewCompressor(flagWorkers)
	if err := compressor.CompressAll(ctx, flagServeDir); err != nil {
		log.Fatalf("Compression failed: %v", err)
	}

	// 6. 清理 repo 目录以减小镜像（保留 .git 用于运行阶段 fetch）
	log.Println("Build phase complete.")
}

// ══════════════════════════════════════════════════════════════
// serve 模式：HTTP 文件服务 + 增量监测 + 按需压缩
// ══════════════════════════════════════════════════════════════
func runServe() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	// 读取构建阶段写入的 commit hash
	commitFile := filepath.Join(flagServeDir, ".last_commit")
	lastCommitBytes, err := os.ReadFile(commitFile)
	if err != nil {
		log.Printf("WARNING: No .last_commit found, will do full diff on first check: %v", err)
	}
	lastCommit := string(lastCommitBytes)
	if lastCommit != "" {
		log.Printf("Resuming from build-time commit: %s", lastCommit)
	}

	// 确保 repo 目录存在（如果构建阶段已保留）
	if _, err := os.Stat(filepath.Join(flagRepoDir, ".git")); os.IsNotExist(err) {
		log.Println("Repo not found, performing initial clone...")
		if err := fullClone(ctx, flagRepoDir); err != nil {
			log.Fatalf("Clone failed: %v", err)
		}
	}

	compressor := NewCompressor(flagWorkers)
	compressor.InitAsyncPool()

	// 解析端口
	port := flagPort
	if port == "" {
		port = os.Getenv("PORT")
	}
	if port == "" {
		port = "80"
	}

	// 启动 HTTP 文件服务器
	server := NewServer(flagServeDir, compressor, port)
	go func() {
		if err := server.Run(ctx); err != nil {
			log.Printf("ERROR: HTTP server: %v", err)
			cancel()
		}
	}()

	// 启动 watcher
	watcher := NewWatcher(repoURL, flagRepoDir, flagServeDir, compressor, pollInterval, lastCommit)
	log.Println("Entering watch loop (interval: 1 min)...")
	watcher.Run(ctx)

	log.Println("=== Haruki Builder Stopped ===")
}

// ══════════════════════════════════════════════════════════════
// 公共辅助函数
// ══════════════════════════════════════════════════════════════

func fullClone(ctx context.Context, repoDir string) error {
	if _, err := os.Stat(repoDir); err == nil {
		log.Println("Removing existing repo directory...")
		if err := os.RemoveAll(repoDir); err != nil {
			return err
		}
	}

	log.Printf("Cloning %s ...", repoURL)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", repoURL, repoDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func getCurrentCommit(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return trimString(string(out)), nil
}

func syncToServeDir(repoDir, serveDir string) error {
	log.Printf("Syncing to %s ...", serveDir)

	if err := os.MkdirAll(serveDir, 0755); err != nil {
		return err
	}

	return filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(repoDir, path)

		if isGitPath(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		destPath := filepath.Join(serveDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		return copyFile(path, destPath)
	})
}

func isGitPath(relPath string) bool {
	return relPath == ".git" || (len(relPath) >= 5 && relPath[:5] == ".git/")
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func trimString(s string) string {
	result := s
	for len(result) > 0 && (result[len(result)-1] == '\n' || result[len(result)-1] == '\r' || result[len(result)-1] == ' ') {
		result = result[:len(result)-1]
	}
	return result
}

// WorkerPool 控制并发
type WorkerPool struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

func NewWorkerPool(size int) *WorkerPool {
	return &WorkerPool{sem: make(chan struct{}, size)}
}

func (p *WorkerPool) Submit(fn func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.sem <- struct{}{}
		defer func() { <-p.sem }()
		fn()
	}()
}

func (p *WorkerPool) Wait() {
	p.wg.Wait()
}
