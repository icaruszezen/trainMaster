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

# 训练节点（trainer/）的测试与检查使用的 Python 虚拟环境，make test-trainer / lint-trainer 自动创建。
# PYTHON 可指定其它解释器（需要 3.10 及以上），如 make test-trainer PYTHON=python3.12。
PYTHON       ?= python3
TRAINER_VENV := trainer/.venv
TRAINER_BIN  := $(CURDIR)/$(TRAINER_VENV)/bin
# 创建虚拟环境时使用的 pip 镜像：环境变量优先，其次 .env（与构建训练节点镜像相同）。
PIP_INDEX_URL ?= $(call dotenv,PIP_INDEX_URL)

# 所有 shell 脚本（包括尚未提交的新文件）；不是 git 检出时用 find。
SHELL_SCRIPTS = $(or $(shell git ls-files --cached --others --exclude-standard -- '*.sh' 2>/dev/null),\
	$(shell find . -name '*.sh' -not -path './.git/*' -not -path '*/node_modules/*' -not -path './data/*' -not -path '*/.venv/*'))
# 优先使用 PATH 中的 shellcheck，其次虚拟环境中的（pip install shellcheck-py）。
SHELLCHECK ?= $(or $(shell command -v shellcheck 2>/dev/null),$(wildcard $(TRAINER_BIN)/shellcheck))

BUILDINFO := github.com/icaruszezen/trainmaster/backend/internal/buildinfo
LDFLAGS   := -s -w \
	$(if $(TM_VERSION),-X $(BUILDINFO).Version=$(TM_VERSION)) \
	$(if $(TM_COMMIT),-X $(BUILDINFO).Commit=$(TM_COMMIT)) \
	-X $(BUILDINFO).BuildTime=$(TM_BUILD_TIME)

.PHONY: help dev-db dev-backend dev-frontend \
	test test-backend test-frontend test-trainer lint lint-backend lint-frontend lint-trainer \
	build build-backend build-frontend docker-build docker-build-trainer setup up down logs ps

help: ## 显示可用命令
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-22s %s\n", $$1, $$2}' $(firstword $(MAKEFILE_LIST))

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

test: test-backend test-frontend test-trainer ## 运行全部测试

test-backend: ## 运行后端测试（设置 TM_TEST_DATABASE_URL 可包含数据库集成测试）
	cd backend && go test ./...

test-frontend: frontend/node_modules ## 运行前端测试
	cd frontend && npm test

# 不需要 GPU、torch 与网络：pytest 用假的 NVML / torch 模块，bash 测试用假的 nvidia-smi / docker。
# test-compose.sh 需要 docker compose（没有时跳过），只解析 docker-compose.yml，不启动容器。
test-trainer: $(TRAINER_VENV)/.installed ## 运行训练节点测试（pytest 与部署脚本的 bash 测试，不需要 GPU）
	cd trainer && $(TRAINER_BIN)/python -m pytest
	@set -e; for t in trainer/scripts/tests/test-*.sh deploy/scripts/tests/test-*.sh; do \
		echo "bash $$t"; bash "$$t"; \
	done

lint: lint-backend lint-frontend lint-trainer ## 代码检查（gofmt、go vet、ESLint、TypeScript、ruff、shellcheck）

lint-backend:
	@cd backend && unformatted="$$(gofmt -l .)" && \
		if [ -n "$$unformatted" ]; then echo "需要 gofmt 的文件：$$unformatted"; exit 1; fi
	cd backend && go vet ./...

lint-frontend: frontend/node_modules
	cd frontend && npm run lint && npm run typecheck

lint-trainer: $(TRAINER_VENV)/.installed ## 训练节点与 shell 脚本检查（ruff、shellcheck）
	cd trainer && $(TRAINER_BIN)/ruff check . && $(TRAINER_BIN)/ruff format --check .
	@if [ -z "$(SHELLCHECK)" ]; then \
		echo "找不到 shellcheck，无法检查 shell 脚本。安装方式（任选其一）："; \
		echo "  Debian / Ubuntu：sudo apt install shellcheck"; \
		echo "  Fedora / RHEL：  sudo dnf install ShellCheck"; \
		echo "  macOS：          brew install shellcheck"; \
		echo "  装到虚拟环境：   $(TRAINER_VENV)/bin/pip install shellcheck-py"; \
		exit 1; \
	fi
	$(SHELLCHECK) -x $(SHELL_SCRIPTS)

# 训练节点的开发环境。先检查解释器：Debian / Ubuntu 的 python3 默认不带 venv 模块，
# 直接 python3 -m venv 会报出不易看懂的 ensurepip 错误。requirements-dev.txt 变化时重新安装。
$(TRAINER_VENV)/.installed: trainer/requirements-dev.txt
	@command -v $(PYTHON) >/dev/null 2>&1 || { \
		echo "找不到 $(PYTHON)：训练节点的测试与检查需要 Python 3.10 及以上。安装方式："; \
		echo "  Debian / Ubuntu：sudo apt install python3 python3-venv"; \
		echo "  Fedora / RHEL：  sudo dnf install python3"; \
		echo "  macOS：          brew install python@3.12"; \
		echo "已安装其它版本时用 PYTHON 指定，如 make test-trainer PYTHON=python3.12"; \
		exit 1; \
	}
	@$(PYTHON) -c 'import sys; sys.exit(sys.version_info < (3, 10))' || { \
		echo "训练节点需要 Python 3.10 及以上，当前 $(PYTHON) 为 $$($(PYTHON) -V 2>&1)。"; \
		echo "安装新版本后用 PYTHON 指定，如 make test-trainer PYTHON=python3.12"; \
		exit 1; \
	}
	@$(PYTHON) -c 'import ensurepip, venv' 2>/dev/null || { \
		echo "$(PYTHON) 缺少 venv / ensurepip 模块，无法创建虚拟环境 $(TRAINER_VENV)。安装方式："; \
		echo "  Debian / Ubuntu：sudo apt install python3-venv（或与版本对应的 python3.X-venv）"; \
		echo "  Fedora / RHEL：  sudo dnf install python3"; \
		exit 1; \
	}
	$(PYTHON) -m venv $(TRAINER_VENV)
	$(TRAINER_BIN)/python -m pip install --disable-pip-version-check -q \
		$(if $(PIP_INDEX_URL),--index-url "$(PIP_INDEX_URL)") -r trainer/requirements-dev.txt
	@touch $@

## ---- 构建 ----

build: build-frontend build-backend ## 构建前端（frontend/dist）与后端二进制（backend/bin/trainmaster）

build-frontend: frontend/node_modules
	cd frontend && npm run build

build-backend:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/trainmaster ./cmd/trainmaster

docker-build: ## 构建应用镜像 trainmaster:$TM_IMAGE_TAG
	$(COMPOSE) build app

# 两个训练节点服务（trainer / trainer-cpu）使用同一个镜像，变体（TM_TORCH_VARIANT）由 make setup 写入 .env。
docker-build-trainer: ## 构建训练节点镜像 trainmaster-trainer:$TM_IMAGE_TAG-$TM_TORCH_VARIANT
	$(COMPOSE) --profile gpu build trainer

## ---- Docker 部署 ----

# 参数用 SETUP_ARGS 传入，如 make setup SETUP_ARGS="--install-toolkit"、SETUP_ARGS="--mode cpu"；
# 全部参数见 bash deploy/scripts/setup-compute.sh --help。
setup: ## 检测本机显卡、驱动与 Docker 的 GPU 支持，把训练节点配置写入 .env
	bash deploy/scripts/setup-compute.sh $(SETUP_ARGS)

# 每次启动前重新检测（换了显卡、升级了驱动或装好 NVIDIA Container Toolkit 后自动切换到 GPU）；
# .env 中 TM_TRAINER_MODE=off 时不运行训练节点。TM_SETUP_CALLER 让脚本按“接下来自动启动”给出提示。
up: export TM_SETUP_CALLER := up
up: setup ## 检测计算资源（同 make setup）后构建并启动全部服务
	$(COMPOSE) up -d --build

down: ## 停止并删除容器（宿主机上的数据目录保留）
	$(COMPOSE) down

logs: ## 查看日志（Ctrl+C 退出）
	$(COMPOSE) logs -f --tail=200

ps: ## 查看服务状态
	$(COMPOSE) ps
