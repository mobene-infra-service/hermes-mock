APP := hermes-mock
EMBED := cmd/hermes-mock/web/dist

.PHONY: tidy web sync-web build build-local dist run clean verify-embed

# 1) 拉取依赖（需有网环境，GOPROXY 建议 goproxy.cn）
tidy:
	go get github.com/emiago/diago@latest
	go get github.com/emiago/sipgo@latest
	go mod tidy

# 2) 构建前端并同步到 go:embed 目录
#    重要：go 二进制嵌入的是 $(EMBED)，不是 web/dist。改了前端必须 make web/sync-web，
#    否则二进制里仍是旧 UI（曾踩坑：直接 npm run build 不会更新嵌入目录）。
web:
	cd web && npm install && npm run build
	$(MAKE) sync-web

# sync-web：仅把已构建的 web/dist 同步到嵌入目录（前端已 build 时用）
sync-web:
	rm -rf $(EMBED)
	mkdir -p cmd/hermes-mock/web
	cp -r web/dist $(EMBED)

# 3) 编译 Linux 二进制（依赖 web，确保嵌入最新前端）
build: web
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o $(APP) ./cmd/hermes-mock

# 交叉编译到 dist/（供 Dockerfile.runtime 快速 e2e；依赖 web）
dist: web
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/hermes-mock-linux ./cmd/hermes-mock

# 本机平台编译（开发自测；依赖 web）
build-local: web
	go build -trimpath -o $(APP) ./cmd/hermes-mock

# verify-embed：校验嵌入目录与最新 web/dist 一致（CI/提交前防呆）
verify-embed:
	@test -d $(EMBED)/assets || { echo "FAIL: $(EMBED) 缺失，请 make web"; exit 1; }
	@diff -rq web/dist $(EMBED) >/dev/null 2>&1 && echo "OK: 嵌入目录与 web/dist 一致" || { echo "FAIL: 嵌入目录与 web/dist 不一致，请 make sync-web"; exit 1; }

# 本地运行（开发，需先 make web 或保证 $(EMBED) 存在）。
# 自动加载 ./.env（如存在；密码等本地变量放那里，样例见 .env.example）。
run:
	@if [ -f .env ]; then set -a && . ./.env && set +a && go run ./cmd/hermes-mock; else go run ./cmd/hermes-mock; fi

clean:
	rm -f $(APP)
	rm -rf $(EMBED) web/dist dist/hermes-mock-linux
