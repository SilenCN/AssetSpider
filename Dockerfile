# AssetSpider 多架构容器镜像（飞牛 / 通用 Linux，amd64 与 arm64）。
#
# 构建阶段统一跑在 BUILDPLATFORM（原生架构），只有最终运行镜像是 TARGETPLATFORM，
# 这样 buildx + QEMU 只需模拟极轻的运行层，Go 交叉编译与 npm 构建都在原生速度完成。
# SQLite 为纯 Go 驱动，CGO_ENABLED=0 静态编译，运行镜像无需 gcc / libc 兼容层。

# ---------- 阶段 1：构建 Vue 前端 ----------
FROM --platform=$BUILDPLATFORM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------- 阶段 2：交叉编译 Go 后端 ----------
FROM --platform=$BUILDPLATFORM golang:1.21-alpine AS builder
WORKDIR /src
# buildx 自动注入的目标架构参数；本地 docker build 时为空，回落到当前平台。
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 \
    GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH:-amd64}" \
    go build -trimpath -ldflags='-s -w' -o /out/assetspider .

# ---------- 阶段 3：最小运行镜像 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/assetspider /app/assetspider
COPY --from=web /web/dist /app/web/dist
COPY conf/config.yaml /app/conf/config.yaml
# data 与日志目录运行时写入；声明为卷便于 NAS 上持久化映射。
RUN mkdir -p /app/data /app/logs
VOLUME ["/app/data", "/app/logs"]
ENV GIN_MODE=release
EXPOSE 8080
# 默认监听 :8080、读取内置配置；容器外按需覆盖 config.yaml 与端口映射。
ENTRYPOINT ["/app/assetspider"]
CMD ["-config", "/app/conf/config.yaml"]
