# trainMaster 常用命令，`make help` 查看说明。
# Windows 下没有 make 时，可直接执行各目标中的命令（见 docs/development.md）。

.DEFAULT_GOAL := help

COMPOSE     ?= docker compose
COMPOSE_DEV := $(COMPOSE) -f docker-compose.yml -f docker-compose.dev.yml

# 从 .env 原样读取单个变量的值（去掉一层引号）。docker compose 自己读取 .env；
# 这里不用 `include .env`，因为 make 会按 makefile 语法解析它（截断 # 之后的内容、
# 展开 $），并把结果导出给 docker compose，覆盖 compose 自己的解析结果。
dotenv = $(shell [ -f .env ] && sed -n 's/^$(1)=//p' .env | tail -n 1 | tr -d '\r' \
	| sed -e 's/^"\(.*\)"$$/\1/' -e "s/^'\(.*\)'$$/\1/")

TM_DEV_DB_PORT ?= $(or $(call dotenv,TM_DEV_DB_PORT),5432)

# 写入二进制/镜像的版本信息（docker compose 通过同名变量传给 Dockerfile）。
TM_VERSION    ?= $(call dotenv,TM_VERSION)
TM_COMMIT     ?= $(or $(call dotenv,TM_COMMIT),$(shell git rev-parse --short HEAD 2>/dev/null))
TM_BUILD_TIME ?= $(or $(call dotenv,TM_BUILD_TIME),$(shell date -u +%Y-%m-%dT%H:%M:%SZ))
export TM_VERSION TM_COMMIT TM_BUILD_TIME

BUILDINFO := github.com/icaruszezen/trainmaster/backend/internal/buildinfo
LDFLAGS   := -s -w \
	$(if $(TM_VERSION),-X $(BUILDINFO).Version=$(TM_VERSION)) \
	$(if $(TM_COMMIT),-X $(BUILDINFO).Commit=$(TM_COMMIT)) \
	-X $(BUILDINFO).BuildTime=$(TM_BUILD_TIME)

.PHONY: help dev-db dev-backend dev-frontend \
	test test-backend test-frontend lint lint-backend lint-frontend \
	build build-backend build-frontend docker-build up down logs ps

help: ## 显示可用命令
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(firstword $(MAKEFILE_LIST))

## ---- 本地开发 ----

# 先以当前用户创建数据目录：否则 Docker 会以 root 身份创建 ./data，dev-backend 无法写入。
dev-db: ## 启动开发用 PostgreSQL（暴露到 127.0.0.1:$TM_DEV_DB_PORT）
	mkdir -p data/postgres data/storage
	$(COMPOSE_DEV) up -d postgres

# 开发后端使用与 compose 中 postgres 相同的账号：环境变量优先，其次 .env。
dev-backend: export TM_DB_USER     ?= $(call dotenv,TM_DB_USER)
dev-backend: export TM_DB_PASSWORD ?= $(call dotenv,TM_DB_PASSWORD)
dev-backend: export TM_DB_NAME     ?= $(call dotenv,TM_DB_NAME)
dev-backend: export TM_LOG_LEVEL   ?= $(call dotenv,TM_LOG_LEVEL)
dev-backend: ## 本地运行后端 http://localhost:8080（需先 make dev-db）
	cd backend && TM_DB_PORT=$(TM_DEV_DB_PORT) TM_DATA_DIR="$(CURDIR)/data/storage" go run ./cmd/trainmaster

dev-frontend: frontend/node_modules ## 本地运行前端 http://localhost:5173（/api 代理到 :8080）
	cd frontend && npm run dev

frontend/node_modules: frontend/package-lock.json
	cd frontend && npm ci
	@touch $@

## ---- 测试与检查 ----

test: test-backend test-frontend ## 运行全部测试

test-backend: ## 运行后端测试（设置 TM_TEST_DATABASE_URL 可包含数据库集成测试）
	cd backend && go test ./...

test-frontend: frontend/node_modules ## 运行前端测试
	cd frontend && npm test

lint: lint-backend lint-frontend ## 代码检查（gofmt、go vet、ESLint、TypeScript）

lint-backend:
	@cd backend && unformatted="$$(gofmt -l .)" && \
		if [ -n "$$unformatted" ]; then echo "需要 gofmt 的文件：$$unformatted"; exit 1; fi
	cd backend && go vet ./...

lint-frontend: frontend/node_modules
	cd frontend && npm run lint && npm run typecheck

## ---- 构建 ----

build: build-frontend build-backend ## 构建前端（frontend/dist）与后端二进制（backend/bin/trainmaster）

build-frontend: frontend/node_modules
	cd frontend && npm run build

build-backend:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trainmaster ./cmd/trainmaster

docker-build: ## 构建应用镜像 trainmaster:$TM_IMAGE_TAG
	$(COMPOSE) build app

## ---- Docker 部署 ----

up: ## 构建并启动全部服务
	$(COMPOSE) up -d --build

down: ## 停止并删除容器（宿主机上的数据目录保留）
	$(COMPOSE) down

logs: ## 查看日志（Ctrl+C 退出）
	$(COMPOSE) logs -f --tail=200

ps: ## 查看服务状态
	$(COMPOSE) ps
