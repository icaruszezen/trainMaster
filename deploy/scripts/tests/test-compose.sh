#!/usr/bin/env bash
# docker-compose.yml 的检查：bash deploy/scripts/tests/test-compose.sh
#
# 优先用真实的 docker compose 解析 docker-compose.yml（不启动任何容器），检查各 profile 下的服务
# （含内置 storage 服务）、x-trainer 锚点合并后的配置、GPU 设备预留与变量插值。
# 当环境未安装 docker 且未设置 TM_REQUIRE_DOCKER=1 时，若存在 python3 + PyYAML 则自动执行静态 YAML
# 结构与插值检查（而非盲目跳过）；设置 TM_REQUIRE_DOCKER=1 时强制要求 docker compose（CI 使用）。
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

command -v python3 >/dev/null 2>&1 || skip "没有 python3"

HAS_DOCKER=0
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
	HAS_DOCKER=1
elif [ "${TM_REQUIRE_DOCKER:-}" = 1 ]; then
	skip "没有安装 docker 或 docker compose 插件"
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-compose.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

if [ "$HAS_DOCKER" -eq 0 ]; then
	if ! python3 -c 'import yaml' >/dev/null 2>&1; then
		skip "没有安装 docker 且缺少 PyYAML"
	fi
	python3 - "$ROOT/docker-compose.yml" <<'PYEOF'
import sys
import yaml

path = sys.argv[1]
with open(path, encoding="utf-8") as f:
    doc = yaml.safe_load(f)

services = doc.get("services") or {}
problems = []

def check(cond, msg):
    if not cond:
        problems.append(msg)

check(sorted(services) == ["app", "postgres", "storage", "trainer", "trainer-cpu"],
      f"services 应包含 app/postgres/storage/trainer/trainer-cpu，实际为 {sorted(services)}")

st = services.get("storage") or {}
check(st.get("build", {}).get("dockerfile") == "deploy/docker/Dockerfile.storage",
      f"storage build.dockerfile 异常: {st.get('build')}")
check("profiles" not in st, "storage 应为默认常驻服务（无 profiles）")
check(st.get("environment", {}).get("TM_STORAGE_DATA_DIR") == "/data",
      "storage 应设置 TM_STORAGE_DATA_DIR=/data")
check(any(str(v).endswith(":/data") for v in st.get("volumes", [])),
      f"storage 应挂载 /data: {st.get('volumes')}")

app = services.get("app") or {}
check("storage" in (app.get("depends_on") or {}), "app depends_on 应包含 storage")
app_env = app.get("environment") or {}
check("TM_STORAGE_SERVICE_URL" in app_env, "app 应配置 TM_STORAGE_SERVICE_URL")
check("TM_STORAGE_SERVICE_TOKEN" in app_env, "app 应配置 TM_STORAGE_SERVICE_TOKEN")

if problems:
    for p in problems:
        print("FAIL [static] " + p, file=sys.stderr)
    sys.exit(1)
PYEOF
	echo "compose: 静态 YAML 检查全部通过（storage、app、postgres、trainer、trainer-cpu）"
	exit 0
fi

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
TM_STORAGE_SERVICE_URL=http://storage:8090
TM_STORAGE_SERVICE_TOKEN=storage-secret-tok
TRAINER_PYTHON_IMAGE=registry.example.com/python:3.12-slim-bookworm
PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple
TORCH_FIND_LINKS=https://mirrors.aliyun.com/pytorch-wheels
DEBIAN_MIRROR=https://mirrors.aliyun.com/debian
TM_HOST_HOSTNAME=gpu-host-01
TM_TRAINER_MODE=cpu
"
# trainer-cpu 在 TM_TORCH_VARIANT 设置时同样使用它（make setup 选择 CPU 时写入 cpu）；
# make setup --variant 固定的变体（TM_TRAINER_VARIANT）传给两个训练节点
config variant "COMPOSE_PROFILES=gpu,cpu
TM_TORCH_VARIANT=cu126
TM_TRAINER_VARIANT=cu126
"
config off "TM_TRAINER_MODE=off
"

# make down 使用的 --profile '*'：列出全部 profile 中的服务（包括 COMPOSE_PROFILES 中没有启用的）
if [ "$fails" -eq 0 ]; then
	printf 'COMPOSE_PROFILES=cpu\n' >"$WORK/down.env"
	services="$(cd "$ROOT" && env -i PATH="$PATH" HOME="${HOME:-/tmp}" \
		docker compose -f docker-compose.yml --env-file "$WORK/down.env" --profile '*' --profile gpu --profile cpu \
		config --services | sort | tr '\n' ' ')"
	if [ "$services" != "app postgres storage trainer trainer-cpu " ]; then
		echo "FAIL [down] --profile '*' 应列出全部服务，实际为：$services" >&2
		fails=$((fails + 1))
	fi
fi

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


def check_storage(name, svc, tag="latest", host_dir=None, token=""):
    check(name, svc.get("image") == f"trainmaster-storage:{tag}", f"storage image 为 {svc.get('image')}")
    build = svc.get("build", {})
    check(name, build.get("dockerfile") == "deploy/docker/Dockerfile.storage", f"storage dockerfile 为 {build}")
    check(name, svc.get("restart") == "unless-stopped", f"storage restart 为 {svc.get('restart')}")
    targets = volume_targets(svc)
    check(name, "/data" in targets, f"storage volumes 为 {targets}")
    if host_dir is not None:
        check(name, targets.get("/data") == host_dir, f"storage /data 挂载为 {targets.get('/data')}")
    check(name, env(svc, "TM_STORAGE_DATA_DIR") == "/data", "TM_STORAGE_DATA_DIR 应为 /data")
    check(name, env(svc, "TM_STORAGE_TOKEN") == token, f"TM_STORAGE_TOKEN 为 {env(svc, 'TM_STORAGE_TOKEN')}")


def check_trainer(name, svc, variant="cu130", tag="latest", shm=8 * 1024**3, service="trainer", mode="auto",
                  hostname="trainmaster-trainer", pinned="auto"):
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
    check(name, env(svc, "TM_TRAINER_SERVICE") == service, f"TM_TRAINER_SERVICE 为 {env(svc, 'TM_TRAINER_SERVICE')}")
    check(name, env(svc, "TM_TRAINER_MODE") == mode, f"TM_TRAINER_MODE 为 {env(svc, 'TM_TRAINER_MODE')}")
    check(name, env(svc, "TM_TRAINER_VARIANT") == pinned, f"TM_TRAINER_VARIANT 为 {env(svc, 'TM_TRAINER_VARIANT')}")
    check(name, svc.get("hostname") == hostname, f"hostname 为 {svc.get('hostname')}")


# 没有 profile：postgres、storage 与 app
s = load("none")
check("none", sorted(s) == ["app", "postgres", "storage"], f"服务为 {sorted(s)}")
check_storage("none", s["storage"])
check("none", s["app"].get("hostname") == "trainmaster", f"app 的 hostname 为 {s['app'].get('hostname')}")
check("none", env(s["app"], "TM_STORAGE_SERVICE_URL") == "http://storage:8090", "app 默认连接本机 storage:8090")
check("none", "TM_NODE_TOKEN" in (s["app"].get("environment") or {}), "app 缺少 TM_NODE_TOKEN")
check("none", env(s["app"], "TM_NODE_TOKEN") == "", "默认不设置令牌")
check("none", env(s["app"], "TM_TRAINER_MODE") == "auto", f"app 的 TM_TRAINER_MODE 为 {env(s['app'], 'TM_TRAINER_MODE')}")

# gpu：trainer 使用全部显卡
s = load("gpu")
check("gpu", sorted(s) == ["app", "postgres", "storage", "trainer"], f"服务为 {sorted(s)}")
check_trainer("gpu", s["trainer"])
check("gpu", gpu_devices(s["trainer"]) == [{"driver": "nvidia", "count": -1, "capabilities": ["gpu"]}],
      f"GPU 设备预留为 {gpu_devices(s['trainer'])}（count: all 合并后应为 -1）")
check("gpu", env(s["trainer"], "TM_SERVER_URL") == "http://app:8080", "默认连接同一 compose 网络中的 app")
check("gpu", "deploy" not in s["app"] and "deploy" not in s["postgres"] and "deploy" not in s["storage"],
      "只有 trainer 预留 GPU")

# cpu：trainer-cpu 不预留 GPU
s = load("cpu")
check("cpu", sorted(s) == ["app", "postgres", "storage", "trainer-cpu"], f"服务为 {sorted(s)}")
check_trainer("cpu", s["trainer-cpu"], variant="cpu", service="trainer-cpu")
check("cpu", gpu_devices(s["trainer-cpu"]) is None, "trainer-cpu 不应预留 GPU")

s = load("both")
check("both", sorted(s) == ["app", "postgres", "storage", "trainer", "trainer-cpu"], f"服务为 {sorted(s)}")

# 变量插值：两个训练节点与 app 使用同一个令牌，storage 与 app 共享 TM_STORAGE_SERVICE_TOKEN
s = load("custom")
check("custom", s["app"].get("hostname") == "gpu-host-01", f"app 的 hostname 为 {s['app'].get('hostname')}")
check_storage("custom/storage", s["storage"], tag="v1", host_dir="/srv/tm/storage", token="storage-secret-tok")
check("custom", env(s["app"], "TM_STORAGE_SERVICE_TOKEN") == "storage-secret-tok", "app 存储服务令牌应来自 .env")
for name in ("trainer", "trainer-cpu"):
    svc = s[name]
    check_trainer(f"custom/{name}", svc, variant="cpu", tag="v1", shm=2 * 1024**3, service=name, mode="cpu",
                  hostname="gpu-host-01")
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
check("custom", env(s["app"], "TM_TRAINER_MODE") == "cpu", f"app 的 TM_TRAINER_MODE 为 {env(s['app'], 'TM_TRAINER_MODE')}")

s = load("variant")
check_trainer("variant/trainer", s["trainer"], variant="cu126", pinned="cu126")
check_trainer("variant/trainer-cpu", s["trainer-cpu"], variant="cu126", service="trainer-cpu", pinned="cu126")

# 关闭了训练节点（make setup --mode off）：没有训练节点，仍有 postgres、storage 与 app
s = load("off")
check("off", sorted(s) == ["app", "postgres", "storage"], f"服务为 {sorted(s)}")
check("off", env(s["app"], "TM_TRAINER_MODE") == "off", f"app 的 TM_TRAINER_MODE 为 {env(s['app'], 'TM_TRAINER_MODE')}")

for p in problems:
    print("FAIL " + p, file=sys.stderr)
sys.exit(1 if problems else 0)
EOF
fi

if [ "$fails" -gt 0 ]; then
	echo "compose: 检查失败" >&2
	exit 1
fi
echo "compose: 全部通过（无 profile、gpu、cpu、全部 profile、storage 集成、关闭训练节点与变量插值）"
