# trainMaster 镜像：前端（Vite 构建产物）+ 后端（Go 单二进制），运行于 Alpine。
#
#   docker build -t trainmaster:latest .
#
# 国内网络可加速依赖下载：
#   --build-arg NPM_REGISTRY=https://registry.npmmirror.com
#   --build-arg GOPROXY=https://goproxy.cn,direct
#   --build-arg ALPINE_MIRROR=https://mirrors.aliyun.com/alpine
# 内网镜像仓库可替换基础镜像：--build-arg NODE_IMAGE=registry.example.com/node:22-alpine 等。

ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.24-alpine
ARG RUNTIME_IMAGE=alpine:3.23

# ---- 1. 前端 ----
FROM ${NODE_IMAGE} AS frontend
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm config set registry "${NPM_REGISTRY}" \
 && npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---- 2. 后端 ----
FROM ${GO_IMAGE} AS backend
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# 未设置或为空的参数不注入，保留源码中的默认值（见 backend/internal/buildinfo）。
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
RUN BI=github.com/icaruszezen/trainmaster/backend/internal/buildinfo \
 && go build -trimpath \
      -ldflags "-s -w ${VERSION:+-X $BI.Version=$VERSION} ${COMMIT:+-X $BI.Commit=$COMMIT} ${BUILD_TIME:+-X $BI.BuildTime=$BUILD_TIME}" \
      -o /out/trainmaster ./cmd/trainmaster

# ---- 3. 运行镜像 ----
FROM ${RUNTIME_IMAGE}
ARG ALPINE_MIRROR
RUN if [ -n "${ALPINE_MIRROR}" ]; then \
      sed -i "s|https://dl-cdn.alpinelinux.org/alpine|${ALPINE_MIRROR}|g" /etc/apk/repositories; \
    fi \
 && apk add --no-cache ca-certificates tzdata su-exec
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
# 去掉 Windows 检出可能带入的 CRLF，并确保可执行。
RUN sed -i 's/\r$//' /usr/local/bin/entrypoint.sh && chmod 0755 /usr/local/bin/entrypoint.sh
COPY --from=backend /out/trainmaster /app/trainmaster
COPY --from=frontend /src/frontend/dist /app/web

ENV TM_HTTP_ADDR=:8080 \
    TM_DATA_DIR=/data \
    TM_WEB_DIR=/app/web \
    TM_LOG_FORMAT=json
WORKDIR /app
EXPOSE 8080
# 启动时可能要等待数据库并执行迁移，留出 start-period。
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/app/trainmaster"]
