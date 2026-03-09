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
# Stage 3: 最终运行镜像 — 体积最小，内嵌 HTTP 服务 + 增量监测
# ══════════════════════════════════════════════════════════════
FROM alpine:3.20

RUN apk add --no-cache \
    git \
    ca-certificates \
    tzdata \
    tini

# 复制二进制
COPY --from=go-builder /haruki-builder /usr/local/bin/haruki-builder

# 复制构建阶段产出的 repo（含 .git，用于增量 fetch）
COPY --from=data-builder /data/repo /data/repo

# 复制已压缩好的静态文件
COPY --from=data-builder /data/serve /data/serve

EXPOSE 80

# serve 模式：内嵌 HTTP 文件服务器 + watcher + 按需压缩
ENTRYPOINT ["tini", "--"]
CMD ["haruki-builder", "-mode=serve", "-repo=/data/repo", "-serve-dir=/data/serve", "-workers=2"]
