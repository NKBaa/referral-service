# syntax=docker/dockerfile:1

# Stage 1: Build binary
FROM golang:1.24-alpine AS builder

WORKDIR /build

# 安装基础编译依赖
RUN apk add --no-cache git ca-certificates

# 下载 Go 模块
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并编译静态二进制
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-w -s" -o /build/referral-service .

# Stage 2: Minimal runtime image
FROM alpine:3.20

# 安装 CA 根证书与 curl（供 Docker Healthcheck 探针使用）
RUN apk --no-cache add ca-certificates tzdata curl && \
    addgroup -g 10001 -S appgroup && \
    adduser -u 10001 -S appuser -G appgroup

WORKDIR /app

# 从构建镜像拷贝二进制文件
COPY --from=builder /build/referral-service /app/referral-service

# 使用 non-root 用户运行（原则 35）
USER appuser:appgroup

EXPOSE 8080

# 容器健康检查（原则 35）
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/referral-service"]
