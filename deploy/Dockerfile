# ---- 1) 构建前端 ----
FROM node:22-alpine AS web
WORKDIR /web
# 跨架构（如在 arm64 宿主上构建 amd64 镜像）时 npm optional 依赖会漏装 rollup native 包，
# 导致 vite build 报 "Cannot find module @rollup/rollup-linux-*"。下面跳过 native 并显式补 musl 版。
ENV ROLLUP_SKIP_NODEJS_NATIVE=1
COPY web/package*.json ./
RUN npm install && \
    npm install --no-save @rollup/rollup-linux-x64-musl @rollup/rollup-linux-arm64-musl || true
COPY web/ ./
RUN npm run build

# ---- 2) 构建 Go（嵌入前端产物）----
FROM golang:1.24-alpine AS build
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct
# go.sum 在 make tidy 之后存在；若首次构建无 go.sum，先在本机 make tidy
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./cmd/hermes-mock/web/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/hermes-mock ./cmd/hermes-mock

# ---- 3) 运行 ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/hermes-mock /app/hermes-mock
COPY assets/ /app/assets/
# 容器内 HTTP 监听 80（平台约定；deploy.yaml 探针/Service/Ingress 全链路 80）。
# 本地不经容器跑（make run）仍是 Go 默认 8080；k8s 配置若覆盖 HTTP_PORT 需同步改 deploy.yaml。
ENV HTTP_PORT=80
# HTTP 配置后台 + SIP 信令（UDP）。RTP 端口段按 RTP_PORT_START/END 另行映射。
EXPOSE 80 5060/udp
ENTRYPOINT ["/app/hermes-mock"]
