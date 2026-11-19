#!/usr/bin/env bash
# 独立存储服务部署配置与入口脚本检查：bash deploy/scripts/tests/test-storage-compose.sh
#
# 检查项：
#   1. deploy/docker/Dockerfile.storage（CGO_ENABLED=0、/healthz、storage-entrypoint.sh）
#   2. deploy/docker/storage-entrypoint.sh（创建受管目录 datasets/exports/.tmp-archives，拒绝非法 PUID/PGID）
#   3. docker-compose.storage.yml（优先用真实 docker compose config，无 docker 时用 PyYAML 静态校验）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-storage-compose.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

fails=0
fail() {
	echo "FAIL [storage-compose] $*" >&2
	fails=$((fails + 1))
}

DOCKERFILE="$ROOT/deploy/docker/Dockerfile.storage"
ENTRYPOINT_SH="$ROOT/deploy/docker/storage-entrypoint.sh"
COMPOSE_FILE="$ROOT/docker-compose.storage.yml"

for f in "$DOCKERFILE" "$ENTRYPOINT_SH" "$COMPOSE_FILE"; do
	if [ ! -f "$f" ]; then
		fail "缺少文件 $f"
	fi
done

if [ ! -x "$ENTRYPOINT_SH" ]; then
	fail "$ENTRYPOINT_SH 缺少可执行权限"
fi

# 1. Dockerfile.storage 关键指令检查
grep -q 'CGO_ENABLED=0' "$DOCKERFILE" || fail "Dockerfile.storage 应启用 CGO_ENABLED=0"
grep -q './cmd/trainmaster-storage' "$DOCKERFILE" || fail "Dockerfile.storage 应构建 ./cmd/trainmaster-storage"
grep -q '/healthz' "$DOCKERFILE" || fail "Dockerfile.storage 应包含 /healthz 健康检查"
grep -q 'storage-entrypoint.sh' "$DOCKERFILE" || fail "Dockerfile.storage 应使用 storage-entrypoint.sh"

# 2. storage-entrypoint.sh 实际执行检查（非 root 模式下创建受管子目录并执行命令；非法 PUID 拒绝启动）
TEST_DATA="$WORK/data-root"
OUT="$(TM_STORAGE_DATA_DIR="$TEST_DATA" PUID=1000 PGID=1000 sh "$ENTRYPOINT_SH" sh -c 'echo entrypoint-ok')"
if [ "$OUT" != "entrypoint-ok" ]; then
	fail "storage-entrypoint.sh 输出异常: $OUT"
fi
for sub in datasets exports .tmp-archives; do
	if [ ! -d "$TEST_DATA/$sub" ]; then
		fail "storage-entrypoint.sh 未创建目录 $TEST_DATA/$sub"
	fi
done

if TM_STORAGE_DATA_DIR="$TEST_DATA" PUID=abc PGID=1000 sh "$ENTRYPOINT_SH" true >/dev/null 2>&1; then
	fail "storage-entrypoint.sh 应拒绝非数字 PUID"
fi

# 3. docker-compose.storage.yml 解析校验
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
	printf 'TM_STORAGE_PORT=18090\nTM_STORAGE_HOST_DIR=/srv/storage-data\nTM_STORAGE_TOKEN=tok123\nTM_IMAGE_TAG=v2\n' >"$WORK/storage.env"
	if ! (cd "$ROOT" && env -i PATH="$PATH" HOME="${HOME:-/tmp}" \
		docker compose -f docker-compose.storage.yml --env-file "$WORK/storage.env" config -q); then
		fail "docker compose -f docker-compose.storage.yml config -q 失败"
	else
		(cd "$ROOT" && env -i PATH="$PATH" HOME="${HOME:-/tmp}" \
			docker compose -f docker-compose.storage.yml --env-file "$WORK/storage.env" config --format json) >"$WORK/storage.json"
		python3 - "$WORK/storage.json" <<'PYEOF' || fails=$((fails + 1))
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    svcs = json.load(f)["services"]

assert list(svcs.keys()) == ["storage"], f"services={list(svcs.keys())}"
st = svcs["storage"]
assert st["image"] == "trainmaster-storage:v2", f"image={st['image']}"
env = st.get("environment") or {}
assert env.get("TM_STORAGE_TOKEN") == "tok123", f"env={env}"
assert env.get("TM_STORAGE_DATA_DIR") == "/data", f"env={env}"
targets = {v["target"]: v["source"] for v in st.get("volumes", [])}
assert targets.get("/data") == "/srv/storage-data", f"volumes={targets}"
PYEOF
	fi
elif [ "${TM_REQUIRE_DOCKER:-}" = 1 ]; then
	fail "TM_REQUIRE_DOCKER=1 但未安装 docker compose"
elif command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; then
	python3 - "$COMPOSE_FILE" <<'PYEOF' || fails=$((fails + 1))
import sys
import yaml

with open(sys.argv[1], encoding="utf-8") as f:
    doc = yaml.safe_load(f)

svcs = doc.get("services") or {}
assert list(svcs.keys()) == ["storage"], f"services={list(svcs.keys())}"
st = svcs["storage"]
assert st.get("build", {}).get("dockerfile") == "deploy/docker/Dockerfile.storage"
env = st.get("environment") or {}
assert env.get("TM_STORAGE_DATA_DIR") == "/data"
assert "TM_STORAGE_TOKEN" in env
assert any(str(v).endswith(":/data") for v in st.get("volumes", []))
PYEOF
fi

if [ "$fails" -gt 0 ]; then
	echo "storage-compose: $fails 项检查失败" >&2
	exit 1
fi
echo "storage-compose: 全部通过（Dockerfile.storage、storage-entrypoint.sh、docker-compose.storage.yml）"
