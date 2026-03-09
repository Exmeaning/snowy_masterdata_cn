package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var preprocessorTriggerFiles = map[string]bool{
	"master/costume3ds.json":         true,
	"master/cardCostume3ds.json":     true,
	"master/costume3dShopItems.json": true,
}

type Watcher struct {
	repoURL      string
	repoDir      string
	serveDir     string
	compressor   *Compressor
	pollInterval time.Duration
	lastCommit   string
}

func NewWatcher(repoURL, repoDir, serveDir string, compressor *Compressor, interval time.Duration, initialCommit string) *Watcher {
	return &Watcher{
		repoURL:      repoURL,
		repoDir:      repoDir,
		serveDir:     serveDir,
		compressor:   compressor,
		pollInterval: interval,
		lastCommit:   initialCommit,
	}
}

func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.check(ctx)
		}
	}
}

func (w *Watcher) check(ctx context.Context) {
	remoteHash, err := w.getRemoteHead(ctx)
	if err != nil {
		log.Printf("WARNING: ls-remote failed: %v", err)
		return
	}

	if remoteHash == w.lastCommit {
		return
	}

	log.Printf("New commit: %s → %s", shorten(w.lastCommit), shorten(remoteHash))

	changed, deleted, err := w.fetchAndDiff(ctx)
	if err != nil {
		log.Printf("ERROR: fetch/diff: %v", err)
		return
	}

	log.Printf("Δ changed=%d deleted=%d", len(changed), len(deleted))

	// 删除
	for _, f := range deleted {
		if isGitPath(f) {
			continue
		}
		w.compressor.RemoveCompressed(filepath.Join(w.serveDir, f))
		log.Printf("  DEL %s", f)
	}

	// 检测是否需要跑预处理
	needPreprocess := false
	for _, f := range changed {
		if preprocessorTriggerFiles[f] {
			needPreprocess = true
			break
		}
	}

	// 复制变更文件 + 清理旧压缩（下次 CDN 请求时按需重新压缩）
	for _, f := range changed {
		if isGitPath(f) {
			continue
		}
		src := filepath.Join(w.repoDir, f)
		dst := filepath.Join(w.serveDir, f)
		if err := copyFile(src, dst); err != nil {
			log.Printf("WARNING: copy %s: %v", f, err)
			continue
		}
		w.compressor.InvalidateCompressed(dst)
	}

	// 预处理
	if needPreprocess {
		log.Println("Trigger files changed → running preprocessor...")
		if err := RunPreprocessor(w.repoDir); err != nil {
			log.Printf("WARNING: preprocessor: %v", err)
		} else {
			gen := "master/snowy_costumes.json"
			src := filepath.Join(w.repoDir, gen)
			dst := filepath.Join(w.serveDir, gen)
			if err := copyFile(src, dst); err != nil {
				log.Printf("WARNING: copy generated: %v", err)
			} else {
				w.compressor.InvalidateCompressed(dst)
			}
		}
		if err := RunMoePreprocessor(w.repoDir); err != nil {
			log.Printf("WARNING: moe preprocessor: %v", err)
		} else {
			gen := "master/moe_costume.json"
			src := filepath.Join(w.repoDir, gen)
			dst := filepath.Join(w.serveDir, gen)
			if err := copyFile(src, dst); err != nil {
				log.Printf("WARNING: copy moe generated: %v", err)
			} else {
				w.compressor.InvalidateCompressed(dst)
			}
		}
	}

	// 更新 commit 记录
	w.lastCommit = remoteHash
	commitFile := filepath.Join(w.serveDir, ".last_commit")
	_ = os.WriteFile(commitFile, []byte(remoteHash), 0644)

	log.Printf("Update complete → %s", shorten(remoteHash))
}

func (w *Watcher) getRemoteHead(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", w.repoDir, "ls-remote", "origin", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 1 {
		return "", fmt.Errorf("unexpected ls-remote output")
	}
	return fields[0], nil
}

func (w *Watcher) fetchAndDiff(ctx context.Context) (changed, deleted []string, err error) {
	oldHash := w.lastCommit

	// fetch，对 shallow repo 用 deepen 策略
	fetch := exec.CommandContext(ctx, "git", "-C", w.repoDir, "fetch", "origin", "--depth=2")
	fetch.Stdout = os.Stdout
	fetch.Stderr = os.Stderr
	if err := fetch.Run(); err != nil {
		deepen := exec.CommandContext(ctx, "git", "-C", w.repoDir, "fetch", "origin", "--deepen=1")
		deepen.Stdout = os.Stdout
		deepen.Stderr = os.Stderr
		if err2 := deepen.Run(); err2 != nil {
			return nil, nil, fmt.Errorf("fetch: %v / deepen: %v", err, err2)
		}
	}

	// reset 到最新
	reset := exec.CommandContext(ctx, "git", "-C", w.repoDir, "reset", "--hard", "origin/HEAD")
	reset.Stdout = os.Stdout
	reset.Stderr = os.Stderr
	if err := reset.Run(); err != nil {
		return nil, nil, fmt.Errorf("reset: %w", err)
	}

	// diff
	if oldHash == "" {
		return w.listAllFiles()
	}

	diff := exec.CommandContext(ctx, "git", "-C", w.repoDir, "diff", "--name-status", oldHash, "HEAD")
	out, err := diff.Output()
	if err != nil {
		log.Printf("WARNING: diff failed (shallow?), fallback to full list: %v", err)
		return w.listAllFiles()
	}

	return parseDiffOutput(string(out))
}

func parseDiffOutput(output string) (changed, deleted []string, err error) {
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		switch {
		case status == "D":
			deleted = append(deleted, parts[1])
		case strings.HasPrefix(status, "R"):
			if len(parts) >= 3 {
				deleted = append(deleted, parts[1])
				changed = append(changed, parts[2])
			}
		default:
			changed = append(changed, parts[len(parts)-1])
		}
	}
	return changed, deleted, sc.Err()
}

func (w *Watcher) listAllFiles() (changed, deleted []string, err error) {
	err = filepath.Walk(w.repoDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(w.repoDir, path)
		if isGitPath(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() {
			changed = append(changed, rel)
		}
		return nil
	})
	return
}

func shorten(hash string) string {
	if len(hash) > 10 {
		return hash[:10]
	}
	return hash
}
