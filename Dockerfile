# syntax=docker/dockerfile:1
# 多阶段构建：前端(node) → 后端(go, 纯静态 CGO_ENABLED=0) → 运行镜像(alpine)。
# 前端 dist 通过 go:embed 烧进二进制，最终镜像只有单文件 + 证书 + 时区。

# ---- 前端构建 ----
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- 后端构建 ----
FROM golang:1.26-alpine AS build
ARG VERSION=docker
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY drivers/ drivers/
COPY internal/ internal/
COPY pkg/ pkg/
COPY web/embed.go web/embed.go
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/pan-transfer ./cmd/server

# ---- 运行镜像 ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/pan-transfer .
# 凭据加密主密钥：不设 PAN_TRANSFER_MASTER_KEY 时自动生成到这里（0600）。
# 备份 /app/data 时必须连这个文件一起保存，否则备份里的凭据无法解密。
ENV TZ=Asia/Shanghai
VOLUME ["/app/data"]
EXPOSE 8935
# 深度健康检查：/api/health 只证明进程活着，/api/health/ready 还会探测
# 数据库与调度器（DB 锁死、调度协程挂掉时这里会失败）。
HEALTHCHECK --interval=60s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8935/api/health/ready >/dev/null 2>&1 || exit 1
ENTRYPOINT ["./pan-transfer"]
CMD ["-addr", ":8935", "-data", "/app/data"]
