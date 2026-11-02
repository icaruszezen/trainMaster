#!/usr/bin/env bash
# docker-compose.yml 的检查：bash deploy/scripts/tests/test-compose.sh
#
# 用真实的 docker compose 解析 docker-compose.yml（不启动任何容器），检查各 profile 下的服务、
# x-trainer 锚点合并后的配置、GPU 设备预留与变量插值。不读取仓库中的 .env 与 docker-compose.override.yml，
# 也不受 shell 中 COMPOSE_PROFILES 等变量的影响。没有 docker compose 或 python3 时跳过
# （设置 TM_REQUIRE_DOCKER=1 时改为失败，CI 使用）。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

skip() {
	if [ "${TM_REQUIRE_DOCKER:-}" = 1 ]; then
		echo "compose: $1" >&2
		exit 1
	fi
	echo "compose: 跳过（$1）"
	exit 0
}
command -v docker >/dev/null 2>&1 || skip "没有安装 docker"
docker compose version >/dev/null 2>&1 || skip "没有 docker compose 插件"
command -v python3 >/dev/null 2>&1 || skip "没有 python3"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-compose.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

fails=0

# config <名称> <env 文件内容>：输出合并后的 JSON 配置到 $WORK/<名称>.json
config() {
	printf '%s' "$2" >"$WORK/$1.env"
	local -a keep=()
	local v
	for v in DOCKER_HOST DOCKER_CONFIG DOCKER_CONTEXT DOCKER_CERT_PATH DOCKER_TLS_VERIFY; do
		if [ -n "${!v:-}" ]; then keep+=("$v=${!v}"); fi
	done
	if ! (cd "$ROOT" && env -i PATH="$PATH" HOME="${HOME:-/tmp}" ${keep[@]+"${keep[@]}"} \
		docker compose -f docker-compose.yml --env-file "$WORK/$1.env" config -q); then
		echo "FAIL [$1] docker compose config -q 失败" >&2
		fails=$((fails + 1))
		return
	fi
	(cd "$ROOT" && env -i PATH="$PATH" HOME="${HOME:-/tmp}" ${keep[@]+"${keep[@]}"} \
		docker compose -f docker-compose.yml --env-file "$WORK/$1.env" config --format json) >"$WORK/$1.json"
}

config none ""
config gpu "COMPOSE_PROFILES=gpu
"
config cpu "COMPOSE_PROFILES=cpu
"
config both "COMPOSE_PROFILES=monitoring,gpu,cpu
"
config custom "COMPOSE_PROFILES=gpu,cpu
TM_TORCH_VARIANT=cpu
TM_IMAGE_TAG=v1
TM_TRAINER_SHM_SIZE=2gb
TM_NODE_TOKEN=abcdefghijklmnop0123
TM_NODE_NAME=gpu-01
TM_SERVER_URL=http://10.0.0.5:8080
TM_HOST_STORAGE_DIR=/srv/tm/storage
TM_HOST_TRAINER_DIR=/srv/tm/trainer
TRAINER_PYTHON_IMAGE=registry.example.com/python:3.12-slim-bookworm
PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple
TORCH_FIND_LINKS=https://mirrors.aliyun.com/pytorch-wheels
DEBIAN_MIRROR=https://mirrors.aliyun.com/debian
"

if [ "$fails" -eq 0 ]; then
	python3 - "$WORK" <<'EOF' || fails=$((fails + 1))
import json
import os
import sys

work = sys.argv[1]
problems = []


def load(name):
    with open(os.path.join(work, name + ".json"), encoding="utf-8") as f:
        return json.load(f)["services"]


def check(name, cond, message):
    if not cond:
        problems.append(f"[{name}] {message}")


def gpu_devices(svc):
    return svc.get("deploy", {}).get("resources", {}).get("reservations", {}).get("devices")


def env(svc, key):
    return (svc.get("environment") or {}).get(key)


def volume_targets(svc):
    return {v["target"]: v["source"] for v in svc.get("volumes", [])}


def check_trainer(name, svc, variant="cu130", tag="latest", shm=8 * 1024**3):
    check(name, svc.get("image") == f"trainmaster-trainer:{tag}-{variant}", f"image 为 {svc.get('image')}")
    build = svc.get("build", {})
    check(name, build.get("context", "").rstrip("/").endswith("/trainer"), f"build.context 为 {build.get('context')}")
    check(name, build.get("args", {}).get("TORCH_VARIANT") == variant, f"build.args 为 {build.get('args')}")
    check(name, int(svc.get("shm_size", 0)) == shm, f"shm_size 为 {svc.get('shm_size')}")
    check(name, svc.get("init") is True, "init 应为 true")
    check(name, "depends_on" not in svc, "训练节点不应依赖 app（其它机器上只运行它）")
    check(name, svc.get("restart") == "unless-stopped", f"restart 为 {svc.get('restart')}")
    check(name, "logging" in svc and svc["logging"].get("driver") == "json-file", "应使用 x-logging 的日志轮转")
    targets = volume_targets(svc)
    check(name, "/data" in targets and "/var/lib/trainmaster" in targets, f"volumes 为 {targets}")
    check(name, env(svc, "TM_DATA_DIR") == "/data", "TM_DATA_DIR 应为 /data")
    for key in ("TM_SERVER_URL", "TM_NODE_TOKEN", "TM_NODE_NAME", "TM_LOG_LEVEL", "PUID", "PGID", "TZ"):
        check(name, key in (svc.get("environment") or {}), f"缺少环境变量 {key}")


# 没有 profile：与之前一样只有 postgres 与 app
s = load("none")
check("none", sorted(s) == ["app", "postgres"], f"服务为 {sorted(s)}")
check("none", "TM_NODE_TOKEN" in (s["app"].get("environment") or {}), "app 缺少 TM_NODE_TOKEN")
check("none", env(s["app"], "TM_NODE_TOKEN") == "", "默认不设置令牌")

# gpu：trainer 使用全部显卡
s = load("gpu")
check("gpu", sorted(s) == ["app", "postgres", "trainer"], f"服务为 {sorted(s)}")
check_trainer("gpu", s["trainer"])
check("gpu", gpu_devices(s["trainer"]) == [{"driver": "nvidia", "count": -1, "capabilities": ["gpu"]}],
      f"GPU 设备预留为 {gpu_devices(s['trainer'])}（count: all 合并后应为 -1）")
check("gpu", env(s["trainer"], "TM_SERVER_URL") == "http://app:8080", "默认连接同一 compose 网络中的 app")
check("gpu", "deploy" not in s["app"] and "deploy" not in s["postgres"], "只有 trainer 预留 GPU")

# cpu：trainer-cpu 不预留 GPU
s = load("cpu")
check("cpu", sorted(s) == ["app", "postgres", "trainer-cpu"], f"服务为 {sorted(s)}")
check_trainer("cpu", s["trainer-cpu"])
check("cpu", gpu_devices(s["trainer-cpu"]) is None, "trainer-cpu 不应预留 GPU")

s = load("both")
check("both", sorted(s) == ["app", "postgres", "trainer", "trainer-cpu"], f"服务为 {sorted(s)}")

# 变量插值：两个训练节点与 app 使用同一个令牌，镜像标签带变体
s = load("custom")
for name in ("trainer", "trainer-cpu"):
    svc = s[name]
    check_trainer(f"custom/{name}", svc, variant="cpu", tag="v1", shm=2 * 1024**3)
    check(name, env(svc, "TM_NODE_TOKEN") == "abcdefghijklmnop0123", "令牌应来自 .env")
    check(name, env(svc, "TM_NODE_NAME") == "gpu-01", "节点名称应来自 .env")
    check(name, env(svc, "TM_SERVER_URL") == "http://10.0.0.5:8080", "平台地址应来自 .env")
    targets = volume_targets(svc)
    check(name, targets.get("/data") == "/srv/tm/storage", f"/data 挂载为 {targets.get('/data')}")
    check(name, targets.get("/var/lib/trainmaster") == "/srv/tm/trainer", "状态目录应来自 TM_HOST_TRAINER_DIR")
    args = svc["build"]["args"]
    check(name, args.get("PYTHON_IMAGE") == "registry.example.com/python:3.12-slim-bookworm", f"PYTHON_IMAGE 为 {args}")
    check(name, args.get("PIP_INDEX_URL") == "https://mirrors.aliyun.com/pypi/simple", "PIP_INDEX_URL")
    check(name, args.get("TORCH_FIND_LINKS") == "https://mirrors.aliyun.com/pytorch-wheels", "TORCH_FIND_LINKS")
    check(name, args.get("DEBIAN_MIRROR") == "https://mirrors.aliyun.com/debian", "DEBIAN_MIRROR")
    check(name, args.get("TORCH_INDEX_URL") == "", "TORCH_INDEX_URL 默认为空")
check("custom", env(s["app"], "TM_NODE_TOKEN") == "abcdefghijklmnop0123", "app 的令牌应来自 .env")

for p in problems:
    print("FAIL " + p, file=sys.stderr)
sys.exit(1 if problems else 0)
EOF
fi

if [ "$fails" -gt 0 ]; then
	echo "compose: 检查失败" >&2
	exit 1
fi
echo "compose: 全部通过（无 profile、gpu、cpu 与变量插值）"
