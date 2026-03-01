# ══════════════════════════════════════════════════════════════
# Stage 1: 编译 Go 二进制
# ══════════════════════════════════════════════════════════════
FROM golang:1.23-alpine AS go-builder

RUN apk add --no-cache git gcc musl-dev

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /haruki-builder .

# ══════════════════════════════════════════════════════════════
# Stage 2: 构建阶段 — clone + 预处理 + 全量 gzip/br 压缩
#   所有 CPU 密集工作在此完成，不消耗生产服务器 CPU
# ══════════════════════════════════════════════════════════════
FROM alpine:3.20 AS data-builder

RUN apk add --no-cache git ca-certificates

COPY --from=go-builder /haruki-builder /usr/local/bin/haruki-builder

# 执行：clone → preprocess → sync → compress
# 全量压缩在 docker build 阶段完成
RUN haruki-builder -mode=build -repo=/data/repo -serve-dir=/data/serve -workers=0

# ══════════════════════════════════════════════════════════════
# Stage 3: 最终运行镜像 — 体积最小，只做增量工作
# ══════════════════════════════════════════════════════════════
FROM alpine:3.20

RUN apk add --no-cache \
    git \
    ca-certificates \
    tzdata \
    caddy \
    tini

# 复制二进制
COPY --from=go-builder /haruki-builder /usr/local/bin/haruki-builder

# 复制构建阶段产出的 repo（含 .git，用于增量 fetch）
COPY --from=data-builder /data/repo /data/repo

# 复制已压缩好的静态文件
COPY --from=data-builder /data/serve /data/serve

# 复制 Caddy 配置
COPY Caddyfile /etc/caddy/Caddyfile

# 入口脚本：同时启动 watcher + caddy
RUN cat > /entrypoint.sh << 'SCRIPT'
#!/bin/sh
set -e

echo "=== Haruki Static Server ==="
echo "Start: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"

# 增量监测进程（后台）
haruki-builder -mode=serve -repo=/data/repo -serve-dir=/data/serve -workers=2 &
BUILDER_PID=$!

# 动态绑定端口（Zeabur 等 PaaS 会传入 PORT 环境变量）
PORT="${PORT:-80}"
sed -i "s/:80 {/:$PORT {/g" /etc/caddy/Caddyfile

# Caddy（后台）
caddy run --config /etc/caddy/Caddyfile &
CADDY_PID=$!

echo "Builder PID=$BUILDER_PID  Caddy PID=$CADDY_PID"

cleanup() {
echo "Shutting down..."
kill $BUILDER_PID 2>/dev/null || true
kill $CADDY_PID 2>/dev/null || true
wait
}
trap cleanup SIGTERM SIGINT

# 等待任意子进程退出
wait -n $BUILDER_PID $CADDY_PID
EXIT_CODE=$?
echo "Process exited ($EXIT_CODE), stopping all..."
cleanup
exit $EXIT_CODE
SCRIPT
RUN chmod +x /entrypoint.sh

EXPOSE 80

# 生产阶段限制 watcher 只用 2 线程压缩，极低 CPU 占用
ENTRYPOINT ["tini", "--"]
CMD ["/entrypoint.sh"]