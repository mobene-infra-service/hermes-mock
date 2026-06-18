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
# Docker 部署默认贴合平台/服务入口习惯：HTTP 80，SIP 15060-15069/udp。
ENV HTTP_PORT=80
# HTTP 配置后台 + SIP 信令（UDP）。RTP 端口段按 RTP_PORT_START/END 另行映射。
EXPOSE 80 15060/udp 15061/udp 15062/udp 15063/udp 15064/udp 15065/udp 15066/udp 15067/udp 15068/udp 15069/udp
ENTRYPOINT ["/app/hermes-mock"]
