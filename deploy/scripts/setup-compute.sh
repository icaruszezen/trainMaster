#!/usr/bin/env bash
# 计算节点自动配置（make setup 调用它；make up 会先执行它）。
#
# 检测本机的 NVIDIA 显卡、驱动与 Docker 的 GPU 支持，决定训练节点以 GPU 还是 CPU 模式运行，
# 按驱动与显卡选择 PyTorch 变体（规则见 trainer/scripts/lib/torch-variant.sh），然后把结果写入 .env：
# COMPOSE_PROFILES（gpu → trainer 服务，cpu → trainer-cpu 服务）、TM_TORCH_VARIANT、TM_NODE_NAME、
# TM_NODE_TOKEN 等，.env 中的其它内容与注释保持不变。
#
# 默认只读取系统信息并修改 .env，不需要 root。只有加 --install-toolkit 时才会（列出命令并确认后）
# 安装 NVIDIA Container Toolkit、修改 Docker 配置并重启 Docker，这一步需要 root 或 sudo。
#
# 用法见 --help。退出码：0 正常（包括要求 GPU 但检查没有通过、只给出警告的情况）；
# 1 执行失败（安装失败、无法写入 .env 等）；2 参数错误。
#
# 测试替身（deploy/scripts/tests/test-setup-compute.sh 使用，平时不需要设置）：
#   TM_NVIDIA_SMI / TM_DOCKER   nvidia-smi / docker 命令
#   TM_SYSFS_ROOT              sysfs 根目录（默认 /sys）：找不到 nvidia-smi 时从 PCI 设备识别显卡
#   TM_CDI_SPECS               NVIDIA 的 CDI 配置文件（空格分隔，默认 /etc/cdi/nvidia.yaml /var/run/cdi/nvidia.yaml）
#   TM_MACHINE                 CPU 架构（默认 uname -m）
#   TM_DOCKER_TEST_TIMEOUT     Docker GPU 验证的超时秒数（默认 120）；TM_DOCKER_PULL_TIMEOUT 拉取镜像的超时（默认 600）
#   TM_DOCKER_WAIT             安装后等待 Docker 重新启动的秒数（默认 30）
# 其余命令（id、sudo、apt-get、dnf、yum、zypper、curl、gpg、tee、nvidia-ctk、systemctl 等）从 PATH 查找。
# shellcheck disable=SC1111 # 中文提示中的引号“”是有意使用的
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
VARIANT_LIB="$ROOT_DIR/trainer/scripts/lib/torch-variant.sh"
EXAMPLE_FILE="$ROOT_DIR/.env.example"

NVIDIA_SMI="${TM_NVIDIA_SMI:-nvidia-smi}"
DOCKER="${TM_DOCKER:-docker}"
SYSFS_ROOT="${TM_SYSFS_ROOT:-/sys}"
CDI_SPECS="${TM_CDI_SPECS:-/etc/cdi/nvidia.yaml /var/run/cdi/nvidia.yaml}"
MACHINE="${TM_MACHINE:-$(uname -m)}"
DOCKER_TEST_TIMEOUT="${TM_DOCKER_TEST_TIMEOUT:-120}"
DOCKER_PULL_TIMEOUT="${TM_DOCKER_PULL_TIMEOUT:-600}"
DOCKER_WAIT="${TM_DOCKER_WAIT:-30}"

DEFAULT_PYTHON_IMAGE=python:3.12-slim-bookworm
DEFAULT_TOOLKIT_REPO=https://nvidia.github.io/libnvidia-container
DEFAULT_SERVER_URL=http://app:8080
TOOLKIT_KEYRING=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
TOOLKIT_DOC_URL=https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
# 追加到 .env 末尾的键放在这个标题下；再次运行时找到它就不再重复添加。
ENV_HEADER="# ---- 计算节点（由 make setup / deploy/scripts/setup-compute.sh 写入，见 docs/deployment.md）----"

# 内部记录的字段分隔符（ASCII 单元分隔符）：不是空白字符，read 不会合并相邻的空字段。
US=$'\037'

TMP_DIR=""
TMP_ENV=""
cleanup() {
	if [ -n "$TMP_ENV" ] && [ -e "$TMP_ENV" ]; then rm -f "$TMP_ENV"; fi
	if [ -n "$TMP_DIR" ]; then rm -rf "$TMP_DIR"; fi
}
trap cleanup EXIT

say() { printf '%s\n' "$*"; }
warn() { printf '警告：%s\n' "$*" >&2; }
error() { printf '错误：%s\n' "$*" >&2; }
fail() {
	error "$@"
	exit 1
}
usage_error() {
	error "$@"
	printf '用法见 %s --help\n' "$0" >&2
	exit 2
}
config_error() {
	error "$@"
	exit 2
}

usage() {
	cat <<EOF
用法：deploy/scripts/setup-compute.sh [选项]
  或：make setup SETUP_ARGS="选项"

检测本机的 NVIDIA 显卡、驱动与 Docker 的 GPU 支持，配置训练节点（写入 .env）。

选项：
  --mode auto|gpu|cpu|off  训练节点模式。默认取 .env 中的 TM_TRAINER_MODE，没有时为 auto。
                           auto：显卡、驱动与 Docker 的 GPU 支持都正常时用 GPU，否则用 CPU；
                           gpu / cpu：强制使用；off：不运行训练节点。指定后同时写入 TM_TRAINER_MODE
  --install-toolkit        Docker 无法使用显卡时安装并配置 NVIDIA Container Toolkit（需要 root 或 sudo）
  --yes                    安装前不再询问确认
  --variant NAME           指定 PyTorch 变体（$(tm_variant_names | grep -vx cpu | tr '\n' ' ' | sed 's/ $//' | sed 's/ / \/ /g')），
                           不按驱动与显卡自动选择
  --server-url URL         平台地址（在其它机器上运行训练节点时），写入 TM_SERVER_URL
  --token TOKEN            节点令牌，与平台 .env 中的 TM_NODE_TOKEN 相同，写入 TM_NODE_TOKEN
  --env-file PATH          要修改的 .env（默认仓库根目录的 .env；不存在时从 .env.example 复制）
  --no-docker-test         不运行 docker run --gpus all 验证，只检查 Docker 的配置
  --dry-run                只检测并打印将写入的内容：不修改任何文件，不安装任何软件
  -h, --help               显示本帮助
EOF
}

# ---- 命令行参数 ----

MODE_ARG=""
INSTALL_TOOLKIT=0
ASSUME_YES=0
ENV_FILE=""
SERVER_URL_ARG=""
TOKEN_ARG=""
VARIANT_ARG=""
DOCKER_TEST=1
DRY_RUN=0

set_opt() {
	[ -n "$2" ] || usage_error "$1 需要一个值"
	case "$1" in
	--mode) MODE_ARG="$2" ;;
	--env-file) ENV_FILE="$2" ;;
	--server-url) SERVER_URL_ARG="$2" ;;
	--token) TOKEN_ARG="$2" ;;
	--variant) VARIANT_ARG="$2" ;;
	esac
}

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--mode | --env-file | --server-url | --token | --variant)
			[ $# -ge 2 ] || usage_error "$1 需要一个值"
			set_opt "$1" "$2"
			shift 2
			;;
		--mode=* | --env-file=* | --server-url=* | --token=* | --variant=*)
			set_opt "${1%%=*}" "${1#*=}"
			shift
			;;
		--install-toolkit)
			INSTALL_TOOLKIT=1
			shift
			;;
		--yes | -y)
			ASSUME_YES=1
			shift
			;;
		--no-docker-test)
			DOCKER_TEST=0
			shift
			;;
		--dry-run)
			DRY_RUN=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) usage_error "未知参数：$1" ;;
		esac
	done
}

validate_args() {
	case "$MODE_ARG" in
	"" | auto | gpu | cpu | off) ;;
	*) usage_error "--mode 只能是 auto、gpu、cpu 或 off（当前为 $MODE_ARG）" ;;
	esac
	if [ -n "$SERVER_URL_ARG" ]; then
		SERVER_URL_ARG="$(strip_trailing_slashes "$SERVER_URL_ARG")"
		valid_url "$SERVER_URL_ARG" || usage_error "--server-url 必须是 http:// 或 https:// 开头的地址（如 http://192.168.1.10:8080）"
	fi
	if [ -n "$TOKEN_ARG" ]; then
		# 平台接受 16–256 个可见 ASCII 字符；这里再排除 compose 解析 .env 时有特殊含义的字符（$、引号、# 等），
		# 这样令牌可以原样写入 .env。make setup 生成的令牌只含字母和数字。
		# 长度单独检查：{16,256} 超过了部分平台正则的重复次数上限（RE_DUP_MAX 为 255）。
		local re='^[A-Za-z0-9._~+/=-]+$'
		[ "${#TOKEN_ARG}" -ge 16 ] && [ "${#TOKEN_ARG}" -le 256 ] && [[ "$TOKEN_ARG" =~ $re ]] || usage_error "--token 必须是 16–256 个字符，只能包含字母、数字和 . _ ~ + / = -（其它字符请直接编辑 .env）"
	fi
	if [ -n "$VARIANT_ARG" ]; then
		if [ "$VARIANT_ARG" = cpu ]; then
			usage_error "CPU 训练节点请使用 --mode cpu"
		fi
		tm_variant_field "$VARIANT_ARG" cuda >/dev/null 2>&1 ||
			usage_error "未知的 PyTorch 变体 $VARIANT_ARG（可选：$(tm_variant_names | grep -vx cpu | tr '\n' ' ' | sed 's/ $//')）"
		case "$MODE_ARG" in cpu | off) usage_error "--variant 只能与 --mode auto 或 gpu 一起使用" ;; esac
	fi
	if [ -n "$ENV_FILE" ]; then
		case "$ENV_FILE" in /*) ;; *) ENV_FILE="$PWD/$ENV_FILE" ;; esac
	else
		ENV_FILE="$ROOT_DIR/.env"
	fi
}

# ---- 小工具 ----

strip_trailing_slashes() {
	local v="$1"
	while [ "${v%/}" != "$v" ]; do v="${v%/}"; done
	printf '%s' "$v"
}

# 只接受不含空白、引号、# 等字符的 http(s) 地址：它们会原样写入 .env 和 shell 命令。
valid_url() {
	local re='^https?://[A-Za-z0-9._~:/?&=%+@-]+$'
	[[ "$1" =~ $re ]]
}

trim() {
	local v="$1"
	v="${v#"${v%%[![:space:]]*}"}"
	v="${v%"${v##*[![:space:]]}"}"
	printf '%s' "$v"
}

# contains_ci <文本> <子串>：忽略大小写包含时返回 0。
contains_ci() {
	grep -qiF -- "$2" <<<"$1"
}

# error_line <输出>：取命令输出中最能说明问题的一行（含 error 的第一行，否则最后一个非空行），最多 300 个字符。
error_line() {
	local line
	line="$(printf '%s\n' "$1" | tr -d '\r' | grep -i -m 1 -E 'error|cannot|denied|failed|unable|not found' || true)"
	if [ -z "$line" ]; then
		line="$(printf '%s\n' "$1" | tr -d '\r' | sed '/^[[:space:]]*$/d' | tail -n 1)"
	fi
	line="$(trim "$line")"
	printf '%s' "${line:0:300}"
}

# mask_secret <值>：只显示前 4 个字符和长度，令牌不完整出现在终端和日志中。
mask_secret() {
	printf '%s…（%d 个字符）' "${1:0:4}" "${#1}"
}

# run_with_timeout <秒> <命令…>：超时返回 124。没有 timeout 命令（如 macOS）时用后台进程计时。
run_with_timeout() {
	local secs="$1"
	shift
	if command -v timeout >/dev/null 2>&1; then
		# GNU timeout 超时返回 124；BusyBox 的 timeout 返回被终止的命令的状态（143），按用时判断。
		local start="$SECONDS" rc=0
		timeout "$secs" "$@" || rc=$?
		case "$rc" in
		124) return 124 ;;
		137 | 143) if [ $((SECONDS - start)) -ge "$secs" ]; then return 124; fi ;;
		esac
		return "$rc"
	fi
	local flag="$TMP_DIR/timed-out.$RANDOM" rc=0 pid watcher
	"$@" &
	pid=$!
	# 计时进程不能继承标准输出，否则 $(...) 会一直等到它结束；命令先结束时计时进程被终止，
	# 它也结束自己的 sleep，不留下后台进程。
	(
		sleeper=""
		trap 'if [ -n "$sleeper" ]; then kill "$sleeper"; fi; exit 0' TERM
		sleep "$secs" &
		sleeper=$!
		wait "$sleeper"
		: >"$flag"
		kill "$pid"
	) >/dev/null 2>&1 &
	watcher=$!
	wait "$pid" || rc=$?
	kill "$watcher" >/dev/null 2>&1 || true
	wait "$watcher" >/dev/null 2>&1 || true
	if [ -e "$flag" ]; then
		rm -f "$flag"
		return 124
	fi
	return "$rc"
}

# env_get <KEY> <文件>：输出文件中最后一个未注释的 KEY=值（去掉一层引号、行尾注释与 \r）；没有时返回 1。
env_get() {
	[ -n "$2" ] && [ -f "$2" ] || return 1
	awk -v k="$1" '
		{ sub(/\r$/, "") }
		$0 ~ ("^[[:space:]]*" k "[[:space:]]*=") {
			v = $0
			sub(/^[^=]*=[[:space:]]*/, "", v)
			if (v ~ /^"/ || v ~ /^'\''/) {
				q = substr(v, 1, 1)
				rest = substr(v, 2)
				end = index(rest, q)
				v = end ? substr(rest, 1, end - 1) : rest
			} else {
				sub(/[[:space:]]+#.*$/, "", v)
				sub(/[[:space:]]+$/, "", v)
			}
			val = v
			found = 1
		}
		END {
			if (found) print val
			exit (found ? 0 : 1)
		}' "$2"
}

# conf_value <KEY> <默认值>：环境变量优先，其次 .env，最后默认值。
conf_value() {
	local key="$1" def="$2" v
	v="${!key:-}"
	if [ -z "$v" ]; then
		v="$(env_get "$key" "$ENV_SOURCE" || true)"
	fi
	printf '%s' "${v:-$def}"
}

is_root() {
	[ "$(id -u 2>/dev/null || echo 1)" = 0 ]
}

node_hostname() {
	local h=""
	if command -v hostname >/dev/null 2>&1; then h="$(hostname 2>/dev/null || true)"; fi
	[ -n "$h" ] || h="$(uname -n 2>/dev/null || true)"
	h="$(trim "$(printf '%s' "$h" | tr -d '\r\n')")"
	# 平台要求节点名称不超过 64 个字符
	printf '%s' "${h:0:64}"
}

# 生成 32 个字符的随机令牌（字母与数字）。去掉 base64 中的 / + = 后可能不足 32 位，因此多读一些并重试。
generate_token() {
	local t="" i
	for i in 1 2 3 4 5; do
		t="$(head -c 48 /dev/urandom | base64 | tr -d '/+=\r\n' | cut -c1-32)"
		if [ "${#t}" -eq 32 ]; then
			printf '%s' "$t"
			return 0
		fi
	done
	fail "无法生成随机令牌（读取 /dev/urandom 失败）"
}

# ---- 显卡与驱动 ----

GPU_LINES=""    # 每块显卡一行：名称、显存、计算能力（未知时为空）、驱动，以 $US 分隔
GPU_COUNT=0
DRIVER=""
NVSMI_STATE=""  # ok / missing / error / no_devices
NVSMI_ERROR=""
CC_UNKNOWN=0
PCI_GPU_COUNT=0
GPU_CCS=""      # 传给 tm_select_variant 的计算能力（空格分隔）

# 没有 nvidia-smi 时，从 PCI 设备判断是否有 NVIDIA 显卡（厂商 0x10de，类别 VGA 0x0300 或 3D 0x0302）。
count_pci_gpus() {
	local n=0 dev vendor class
	for dev in "$SYSFS_ROOT"/bus/pci/devices/*; do
		[ -r "$dev/vendor" ] && [ -r "$dev/class" ] || continue
		vendor="$(tr -d '[:space:]' <"$dev/vendor")"
		class="$(tr -d '[:space:]' <"$dev/class")"
		[ "$vendor" = 0x10de ] || continue
		case "$class" in 0x0300* | 0x0302*) n=$((n + 1)) ;; esac
	done
	printf '%s' "$n"
}

detect_gpus() {
	PCI_GPU_COUNT="$(count_pci_gpus)"
	if ! command -v "$NVIDIA_SMI" >/dev/null 2>&1; then
		NVSMI_STATE=missing
		return 0
	fi
	local out rc=0 has_cc=1
	out="$("$NVIDIA_SMI" --query-gpu=name,driver_version,compute_cap,memory.total --format=csv,noheader 2>&1)" || rc=$?
	if [ "$rc" -ne 0 ] && { contains_ci "$out" compute_cap || contains_ci "$out" 'not a valid field'; }; then
		# 旧版 nvidia-smi（驱动 510 之前）不能查询计算能力：只按驱动版本选择变体。
		has_cc=0
		CC_UNKNOWN=1
		rc=0
		out="$("$NVIDIA_SMI" --query-gpu=name,driver_version,memory.total --format=csv,noheader 2>&1)" || rc=$?
	fi
	out="$(printf '%s\n' "$out" | tr -d '\r')"
	if [ "$rc" -ne 0 ]; then
		if contains_ci "$out" 'no devices were found'; then
			NVSMI_STATE=no_devices
		else
			NVSMI_STATE=error
		fi
		NVSMI_ERROR="$(error_line "$out")"
		return 0
	fi
	# 名称里理论上可能有逗号，所以从右往左取驱动、计算能力、显存。
	GPU_LINES="$(printf '%s\n' "$out" | awk -F', *' -v has_cc="$has_cc" -v us="$US" '
		{
			for (i = 1; i <= NF; i++) gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i)
			n = has_cc ? 3 : 2
			if (NF < n + 1 || $1 == "") next
			name = $1
			for (i = 2; i <= NF - n; i++) name = name ", " $i
			driver = $(NF - n + 1)
			cc = has_cc ? $(NF - 1) : ""
			if (cc !~ /^[0-9]+\.[0-9]+$/) cc = ""
			print name us $NF us cc us driver
		}')"
	if [ -z "$GPU_LINES" ]; then
		NVSMI_STATE=no_devices
		return 0
	fi
	NVSMI_STATE=ok
	GPU_COUNT="$(printf '%s\n' "$GPU_LINES" | wc -l | tr -d ' ')"
	DRIVER="$(printf '%s\n' "$GPU_LINES" | sed -n 1p | cut -d "$US" -f4)"
	local cc
	GPU_CCS=""
	while IFS= read -r cc; do
		if [ -z "$cc" ]; then
			CC_UNKNOWN=1
			cc=7.5 # 计算能力未知时假定为 Turing（7.5）或更新的显卡
		fi
		GPU_CCS="${GPU_CCS:+$GPU_CCS }$cc"
	done <<EOF
$(printf '%s\n' "$GPU_LINES" | cut -d "$US" -f3)
EOF
}

host_gpu_ok() {
	[ "$NVSMI_STATE" = ok ] && [ "$GPU_COUNT" -gt 0 ]
}

# 显存：nvidia-smi 输出 "24564 MiB"，显示为 GiB。
format_memory() {
	case "$1" in
	*[0-9]" MiB") awk -v m="${1% MiB}" 'BEGIN { printf "%.1f GiB", m / 1024 }' ;;
	"" | *"N/A"*) printf '未知' ;;
	*) printf '%s' "$1" ;;
	esac
}

report_gpus() {
	say "显卡与驱动："
	case "$NVSMI_STATE" in
	ok)
		say "  检测到 $GPU_COUNT 块 NVIDIA 显卡，驱动 $DRIVER"
		local i=0 name mem cc rest
		while IFS="$US" read -r name mem cc rest; do
			say "    GPU $i：$name · 显存 $(format_memory "$mem") · 计算能力 ${cc:-未知}"
			i=$((i + 1))
		done <<EOF
$GPU_LINES
EOF
		if [ "$CC_UNKNOWN" = 1 ]; then
			say "  nvidia-smi 没有给出部分显卡的计算能力（驱动较旧），按驱动版本选择 PyTorch 变体（假定为 Turing（7.5）或更新的显卡）"
		fi
		;;
	missing)
		if [ "$PCI_GPU_COUNT" -gt 0 ]; then
			say "  检测到 $PCI_GPU_COUNT 块 NVIDIA 显卡（PCI 设备），但没有安装 NVIDIA 驱动（找不到 nvidia-smi）"
			say "  需要先安装 NVIDIA 驱动：Ubuntu 可运行 sudo ubuntu-drivers install，其它发行版见发行版或 NVIDIA 的说明；装好后重启机器，再运行 make setup"
		else
			say "  未检测到 NVIDIA 显卡"
		fi
		;;
	no_devices)
		say "  已安装 NVIDIA 驱动，但 nvidia-smi 没有找到显卡${NVSMI_ERROR:+（$NVSMI_ERROR）}"
		;;
	error)
		say "  nvidia-smi 运行失败：$NVSMI_ERROR"
		if contains_ci "$NVSMI_ERROR" mismatch; then
			say "  驱动更新后内核模块还是旧版本（Driver/library version mismatch）：重启机器（或重新加载 nvidia 内核模块）后再运行 make setup"
		else
			say "  请确认 NVIDIA 驱动安装正确（nvidia-smi 能正常运行）后再运行 make setup"
		fi
		;;
	esac
}

# ---- Docker ----

DOCKER_STATE=""   # ok / missing / unreachable
DOCKER_ERROR=""
DOCKER_DESKTOP=0
NV_RUNTIME=0
CDI_SPEC=""
TOOLKIT_INSTALLED=0
STATIC_GPU=0      # Docker 的配置看起来支持 GPU（nvidia 运行时、Docker Desktop、CDI 或已安装 Toolkit 的 hook）
DOCKER_GPU=""     # ok / unsupported / failed / unverified / untested / 空（不适用）
DOCKER_GPU_DETAIL=""
CONTAINER_GPU_COUNT=0

detect_docker() {
	DOCKER_STATE="" DOCKER_ERROR="" DOCKER_DESKTOP=0 NV_RUNTIME=0 CDI_SPEC="" TOOLKIT_INSTALLED=0 STATIC_GPU=0
	if ! command -v "$DOCKER" >/dev/null 2>&1; then
		DOCKER_STATE=missing
		return 0
	fi
	local out rc=0 os runtimes f
	# shellcheck disable=SC2016 # Go 模板，由 docker 解析
	out="$("$DOCKER" info --format '{{.OperatingSystem}}|{{range $k, $v := .Runtimes}}{{$k}} {{end}}' 2>"$TMP_DIR/docker-info.err")" || rc=$?
	if [ "$rc" -ne 0 ]; then
		DOCKER_STATE=unreachable
		DOCKER_ERROR="$(error_line "$(cat "$TMP_DIR/docker-info.err") $out")"
		return 0
	fi
	DOCKER_STATE=ok
	out="$(printf '%s' "$out" | tr -d '\r\n')"
	os="${out%%|*}"
	runtimes="${out#*|}"
	case "$os" in *"Docker Desktop"*) DOCKER_DESKTOP=1 ;; esac
	case " $runtimes " in *" nvidia "*) NV_RUNTIME=1 ;; esac
	for f in $CDI_SPECS; do
		if [ -f "$f" ]; then
			CDI_SPEC="$f"
			break
		fi
	done
	if command -v nvidia-container-runtime-hook >/dev/null 2>&1 || command -v nvidia-ctk >/dev/null 2>&1; then
		TOOLKIT_INSTALLED=1
	fi
	if [ "$NV_RUNTIME" = 1 ] || [ "$DOCKER_DESKTOP" = 1 ] || [ -n "$CDI_SPEC" ] ||
		command -v nvidia-container-runtime-hook >/dev/null 2>&1; then
		STATIC_GPU=1
	fi
}

# 实际验证：docker run --rm --gpus all <镜像> nvidia-smi -L。镜像就是构建训练节点用的基础镜像，
# nvidia-smi 由 NVIDIA Container Toolkit 从宿主机挂载进容器。
docker_gpu_test() {
	DOCKER_GPU_DETAIL=""
	CONTAINER_GPU_COUNT=0
	local image="$PYTHON_IMAGE" out rc=0 name="trainmaster-gpu-check-$$"
	if ! "$DOCKER" image inspect "$image" >/dev/null 2>&1; then
		say "  正在拉取 $image（构建训练节点镜像也要用它），用于验证 Docker 能否使用显卡…"
		out="$(run_with_timeout "$DOCKER_PULL_TIMEOUT" "$DOCKER" pull -q "$image" 2>&1)" || rc=$?
		if [ "$rc" -ne 0 ]; then
			local why
			why="$(error_line "$out")"
			if [ "$rc" -eq 124 ]; then why="超过 $DOCKER_PULL_TIMEOUT 秒没有完成"; fi
			DOCKER_GPU=unverified
			DOCKER_GPU_DETAIL="无法拉取镜像 $image（${why:-退出码 $rc}；离线或无法访问镜像仓库？），不能实际验证 Docker 能否使用显卡"
			return 0
		fi
	fi
	rc=0
	out="$(run_with_timeout "$DOCKER_TEST_TIMEOUT" "$DOCKER" run --rm --name "$name" --gpus all "$image" nvidia-smi -L 2>&1)" || rc=$?
	out="$(printf '%s\n' "$out" | tr -d '\r')"
	if [ "$rc" -eq 0 ]; then
		CONTAINER_GPU_COUNT="$(printf '%s\n' "$out" | grep -c '^GPU [0-9]' || true)"
		if [ "$CONTAINER_GPU_COUNT" -gt 0 ]; then
			DOCKER_GPU=ok
		else
			DOCKER_GPU=failed
			DOCKER_GPU_DETAIL="容器中的 nvidia-smi -L 没有列出任何显卡"
		fi
		return 0
	fi
	if [ "$rc" -eq 124 ]; then
		"$DOCKER" rm -f "$name" >/dev/null 2>&1 || true
		DOCKER_GPU=failed
		DOCKER_GPU_DETAIL="docker run 超过 $DOCKER_TEST_TIMEOUT 秒没有结束"
		return 0
	fi
	# Docker 找不到 NVIDIA 的支持：较早的版本报 could not select device driver "nvidia"；
	# 较新的版本（经 CDI 实现 --gpus）报 failed to discover GPU vendor from CDI / unresolvable CDI devices。
	if contains_ci "$out" 'could not select device driver' || contains_ci "$out" 'failed to discover GPU vendor' ||
		contains_ci "$out" 'no known GPU vendor' || contains_ci "$out" 'unresolvable CDI devices'; then
		DOCKER_GPU=unsupported
	else
		DOCKER_GPU=failed
	fi
	DOCKER_GPU_DETAIL="$(error_line "$out")"
}

report_docker_state() {
	case "$DOCKER_STATE" in
	ok) ;;
	missing)
		say "Docker：未安装（找不到 $DOCKER 命令），安装方法见 https://docs.docker.com/engine/install/"
		;;
	unreachable)
		say "Docker：无法连接 Docker 守护进程：$DOCKER_ERROR"
		say "  确认 Docker 正在运行；当前用户没有权限时，把它加入 docker 组（sudo usermod -aG docker \$USER，重新登录后生效）或用 sudo 运行"
		;;
	esac
}

report_docker_gpu() {
	[ "$DOCKER_STATE" = ok ] || return 0
	local how=""
	if [ "$NV_RUNTIME" = 1 ]; then how="已配置 nvidia 运行时"; fi
	if [ "$DOCKER_DESKTOP" = 1 ]; then how="${how:+$how，}Docker Desktop"; fi
	if [ -n "$CDI_SPEC" ]; then how="${how:+$how，}CDI 配置 $CDI_SPEC"; fi
	case "$DOCKER_GPU" in
	ok)
		say "Docker GPU 支持：正常（docker run --gpus all $PYTHON_IMAGE nvidia-smi -L 看到 $CONTAINER_GPU_COUNT 块显卡）"
		if [ "$CONTAINER_GPU_COUNT" -lt "$GPU_COUNT" ]; then
			warn "宿主机有 $GPU_COUNT 块显卡，容器中只看到 $CONTAINER_GPU_COUNT 块"
		fi
		;;
	unsupported)
		say "Docker GPU 支持：不可用（$DOCKER_GPU_DETAIL）"
		if [ "$TOOLKIT_INSTALLED" = 1 ]; then
			say "  已安装 NVIDIA Container Toolkit，但 Docker 还没有配置或重启"
		else
			say "  没有安装 NVIDIA Container Toolkit"
		fi
		;;
	failed)
		say "Docker GPU 支持：验证失败（$DOCKER_GPU_DETAIL）"
		if contains_ci "$DOCKER_GPU_DETAIL" mismatch; then
			say "  驱动更新后内核模块还是旧版本：重启机器后再运行 make setup"
		fi
		;;
	unverified)
		say "Docker GPU 支持：无法验证（$DOCKER_GPU_DETAIL）"
		if [ "$STATIC_GPU" = 1 ]; then
			say "  Docker 的配置看起来支持 GPU（${how:-已安装 NVIDIA Container Toolkit}），按支持处理"
		else
			say "  Docker 中没有找到 NVIDIA 的配置（nvidia 运行时、CDI 或 NVIDIA Container Toolkit）"
		fi
		;;
	untested)
		if [ "$STATIC_GPU" = 1 ]; then
			say "Docker GPU 支持：已配置（${how:-已安装 NVIDIA Container Toolkit}；--no-docker-test，未实际验证）"
		else
			say "Docker GPU 支持：未配置（没有 nvidia 运行时、CDI 或 NVIDIA Container Toolkit；--no-docker-test，未实际验证）"
		fi
		;;
	esac
}

docker_gpu_usable() {
	case "$DOCKER_GPU" in
	ok) return 0 ;;
	unverified | untested) [ "$STATIC_GPU" = 1 ] ;;
	*) return 1 ;;
	esac
}

# Docker Engine（不是 Docker Desktop）没有 GPU 支持、需要安装 NVIDIA Container Toolkit 时返回 0。
toolkit_needed() {
	host_gpu_ok && [ "$DOCKER_STATE" = ok ] && [ "$DOCKER_DESKTOP" = 0 ] || return 1
	case "$DOCKER_GPU" in
	unsupported) return 0 ;;
	unverified | untested) [ "$STATIC_GPU" = 0 ] ;;
	*) return 1 ;;
	esac
}

# ---- NVIDIA Container Toolkit ----

detect_pkg_manager() {
	local pm
	for pm in apt-get dnf yum zypper; do
		if command -v "$pm" >/dev/null 2>&1; then
			printf '%s' "$pm"
			return 0
		fi
	done
	return 1
}

# toolkit_commands <包管理器> <sudo 前缀>：安装与配置命令，每行一条，与 NVIDIA 官方安装文档一致：
#   $TOOLKIT_DOC_URL
# 仓库地址可用 NVIDIA_TOOLKIT_REPO 换成镜像（同时把下载的仓库文件中的地址替换为镜像）。
# 与文档的差别：curl 加 -f（HTTP 错误时不把错误页写进软件源），gpg 加 --batch --yes（重复运行时覆盖密钥文件）。
toolkit_commands() {
	local pm="$1" s="$2" repo="$TOOLKIT_REPO" mirror_sed=""
	if [ "$repo" != "$DEFAULT_TOOLKIT_REPO" ]; then
		mirror_sed=" -e 's#$DEFAULT_TOOLKIT_REPO#$repo#g'"
	fi
	case "$pm" in
	apt-get)
		if ! command -v curl >/dev/null 2>&1 || ! command -v gpg >/dev/null 2>&1; then
			printf '%s\n' "${s}apt-get update" "${s}apt-get install -y --no-install-recommends curl gnupg2"
		fi
		printf '%s\n' \
			"curl -fsSL $repo/gpgkey | ${s}gpg --batch --yes --dearmor -o $TOOLKIT_KEYRING" \
			"curl -fsSL $repo/stable/deb/nvidia-container-toolkit.list | sed -e 's#deb https://#deb [signed-by=$TOOLKIT_KEYRING] https://#g'$mirror_sed | ${s}tee /etc/apt/sources.list.d/nvidia-container-toolkit.list" \
			"${s}apt-get update" \
			"${s}apt-get install -y nvidia-container-toolkit"
		;;
	dnf | yum)
		if ! command -v curl >/dev/null 2>&1; then
			printf '%s\n' "${s}$pm install -y curl"
		fi
		printf '%s\n' \
			"curl -fsSL $repo/stable/rpm/nvidia-container-toolkit.repo${mirror_sed:+ | sed$mirror_sed} | ${s}tee /etc/yum.repos.d/nvidia-container-toolkit.repo" \
			"${s}$pm install -y nvidia-container-toolkit"
		;;
	zypper)
		printf '%s\n' \
			"zypper lr nvidia-container-toolkit >/dev/null 2>&1 || ${s}zypper --non-interactive ar $repo/stable/rpm/nvidia-container-toolkit.repo" \
			"${s}zypper --non-interactive --gpg-auto-import-keys install -y nvidia-container-toolkit"
		;;
	esac
	printf '%s\n' "${s}nvidia-ctk runtime configure --runtime=docker"
	if command -v systemctl >/dev/null 2>&1; then
		printf '%s\n' "${s}systemctl restart docker"
	elif command -v service >/dev/null 2>&1; then
		printf '%s\n' "${s}service docker restart"
	fi
}

sudo_prefix() {
	if is_root; then
		printf ''
	else
		printf 'sudo '
	fi
}

print_toolkit_hint() {
	local pm s
	s="$(sudo_prefix)"
	say "  Docker 需要 NVIDIA Container Toolkit 才能使用显卡。自动安装（需要 root 或 sudo，执行前列出命令并询问确认）："
	say "    make setup SETUP_ARGS=--install-toolkit"
	say "    （或 deploy/scripts/setup-compute.sh --install-toolkit）"
	if pm="$(detect_pkg_manager)"; then
		say "  也可以手动执行（$pm）："
	else
		pm=apt-get
		say "  也可以手动安装（下面是 Debian / Ubuntu 的命令，其它发行版见 $TOOLKIT_DOC_URL）："
	fi
	toolkit_commands "$pm" "$s" | sed 's/^/    /'
	if ! command -v systemctl >/dev/null 2>&1 && ! command -v service >/dev/null 2>&1; then
		say "    然后重启 Docker"
	fi
}

wait_for_docker() {
	local i=0
	while [ "$i" -lt "$DOCKER_WAIT" ]; do
		if "$DOCKER" info >/dev/null 2>&1; then return 0; fi
		sleep 1
		i=$((i + 1))
	done
	return 1
}

# 返回 0：已安装并配置；1：失败；2：没有执行（用户取消或 --dry-run）。
install_toolkit() {
	local pm s="" cmds cmd answer
	if ! pm="$(detect_pkg_manager)"; then
		error "无法识别包管理器（支持 apt-get、dnf、yum、zypper），请按 NVIDIA 官方文档手动安装：$TOOLKIT_DOC_URL"
		return 1
	fi
	if ! is_root; then
		if ! command -v sudo >/dev/null 2>&1; then
			error "安装 NVIDIA Container Toolkit 需要 root 权限：请以 root 运行，或先安装 sudo"
			return 1
		fi
		s="sudo "
	fi
	cmds="$(toolkit_commands "$pm" "$s")"
	say "将执行以下命令安装 NVIDIA Container Toolkit 并配置 Docker（会重启 Docker，正在运行的容器随之重启）："
	printf '%s\n' "$cmds" | sed 's/^/  /'
	if [ "$DRY_RUN" = 1 ]; then
		say "（--dry-run：不执行）"
		return 2
	fi
	if [ "$ASSUME_YES" != 1 ]; then
		printf '继续执行？[y/N] '
		answer=""
		read -r answer || true
		case "$answer" in
		[yY] | [yY][eE][sS]) ;;
		*)
			say "已取消安装。"
			return 2
			;;
		esac
	fi
	while IFS= read -r cmd; do
		[ -n "$cmd" ] || continue
		say "+ $cmd"
		# 命令的标准输入接 /dev/null：不能读走本循环的输入，apt 等也不会停下来等待输入。
		if ! bash -c "set -o pipefail; $cmd" </dev/null; then
			error "命令执行失败：$cmd"
			return 1
		fi
	done <<EOF
$cmds
EOF
	if ! command -v systemctl >/dev/null 2>&1 && ! command -v service >/dev/null 2>&1; then
		warn "找不到 systemctl / service，请手动重启 Docker 后再运行 make setup"
	fi
	return 0
}

# ---- .env ----

ENV_SOURCE=""   # 读取当前值的文件：.env；--dry-run 且 .env 不存在时为 .env.example
SET_KEYS=""     # 要写入的键（按写入顺序）
CHANGES=""      # 有变化的键，每行：KEY、原来是否有这一行（1/0）、原值、新值，以 $US 分隔

set_key() { # set_key <KEY> <值>
	local key="$1" value="$2" old existed=1
	SET_KEYS="${SET_KEYS:+$SET_KEYS }$key"
	printf -v "NEW_$key" '%s' "$value"
	old="$(env_get "$key" "$ENV_SOURCE")" || existed=0
	if [ "$existed" = 0 ] || [ "$old" != "$value" ]; then
		CHANGES="$CHANGES$key$US$existed$US$old$US$value
"
	fi
}

display_value() { # display_value <KEY> <值>：令牌打码
	if [ -z "$2" ]; then
		printf '（空）'
	elif [ "$1" = TM_NODE_TOKEN ]; then
		mask_secret "$2"
	else
		printf '%s' "$2"
	fi
}

# 生成新的 .env 内容：替换已有的 KEY= 行（未注释的；保留行尾的 \r），其余行原样保留；
# 没有的键追加到末尾的标题下。值通过环境变量传给 awk，避免 awk -v 处理反斜杠转义。
render_env() {
	local key var
	for key in $SET_KEYS; do
		var="NEW_$key"
		export "_TM_SET_$key=${!var}"
	done
	_TM_SET_KEYS="$SET_KEYS" _TM_HEADER="$ENV_HEADER" awk '
		BEGIN {
			n = split(ENVIRON["_TM_SET_KEYS"], keys, " ")
			header = ENVIRON["_TM_HEADER"]
		}
		{
			line = $0
			cr = ""
			if (sub(/\r$/, "", line)) cr = "\r"
			if (line == header) has_header = 1
			out = $0
			for (i = 1; i <= n; i++) {
				k = keys[i]
				if (line ~ ("^[[:space:]]*" k "[[:space:]]*=")) {
					out = k "=" ENVIRON["_TM_SET_" k] cr
					done[k] = 1
					break
				}
			}
			print out
			lines++
			last_blank = (line ~ /^[[:space:]]*$/)
		}
		END {
			first = 1
			for (i = 1; i <= n; i++) {
				k = keys[i]
				if (k in done) continue
				if (first && !has_header) {
					if (lines > 0 && !last_blank) print ""
					print header
				}
				first = 0
				print k "=" ENVIRON["_TM_SET_" k]
			}
		}' "${ENV_SOURCE:-/dev/null}"
}

same_file() {
	if command -v cmp >/dev/null 2>&1; then
		cmp -s "$1" "$2"
	else
		[ "$(cat "$1"; printf x)" = "$(cat "$2"; printf x)" ]
	fi
}

# 写入 .env：先写同目录的临时文件再改名（读取方不会看到写了一半的文件），保留原文件的权限与属主。
write_env() {
	local rendered="$TMP_DIR/env.new"
	render_env >"$rendered"
	if [ -f "$ENV_FILE" ] && same_file "$rendered" "$ENV_FILE"; then
		ENV_WRITTEN=unchanged
		return 0
	fi
	if [ "$DRY_RUN" = 1 ]; then
		ENV_WRITTEN=dry-run
		return 0
	fi
	if [ -L "$ENV_FILE" ]; then
		# 符号链接：写入它指向的文件，保留链接本身
		cat "$rendered" >"$ENV_FILE" || fail "无法写入 $ENV_FILE"
	else
		local base
		base="$(basename "$ENV_FILE")"
		TMP_ENV="$(mktemp "$(dirname "$ENV_FILE")/.${base#.}.setup.XXXXXX")" ||
			fail "无法在 $(dirname "$ENV_FILE") 中创建临时文件"
		cp -p "$ENV_FILE" "$TMP_ENV" 2>/dev/null || true
		cat "$rendered" >"$TMP_ENV" || fail "无法写入临时文件 $TMP_ENV"
		mv -f "$TMP_ENV" "$ENV_FILE" || fail "无法更新 $ENV_FILE"
		TMP_ENV=""
	fi
	ENV_WRITTEN=written
}

# 切换 GPU / CPU 模式（或关闭）后，删除另一个模式留下的训练节点容器：两者使用同一个状态目录，
# 也就是同一个节点标识，同时运行会互相覆盖平台上的节点信息。
remove_stale_trainers() {
	local old_profiles="$1" new_profiles="$2" p svc ids
	[ "$DRY_RUN" = 0 ] && [ "$DOCKER_STATE" = ok ] || return 0
	for p in gpu cpu; do
		case ",$old_profiles," in *",$p,"*) ;; *) continue ;; esac
		case ",$new_profiles," in *",$p,"*) continue ;; esac
		if [ "$p" = gpu ]; then svc=trainer; else svc=trainer-cpu; fi
		ids="$(cd "$ROOT_DIR" && "$DOCKER" compose --env-file "$ENV_FILE" ps -aq "$svc" 2>/dev/null || true)"
		[ -n "$ids" ] || continue
		say "删除之前的训练节点容器（$svc 服务）…"
		if ! (cd "$ROOT_DIR" && "$DOCKER" compose --env-file "$ENV_FILE" rm -s -f "$svc"); then
			warn "无法删除 $svc 服务的容器，请手动执行：docker compose rm -s -f $svc"
		fi
	done
}

# merge_profiles <原 COMPOSE_PROFILES> <gpu|cpu|off>：保留用户的其它 profile，去掉 gpu / cpu 后按结果加入。
merge_profiles() {
	local p out="" IFS=,
	for p in $1; do
		p="$(trim "$p")"
		case "$p" in "" | gpu | cpu) continue ;; esac
		case ",$out," in *",$p,"*) continue ;; esac
		out="${out:+$out,}$p"
	done
	case "$2" in gpu | cpu) out="${out:+$out,}$2" ;; esac
	printf '%s' "$out"
}

# ---- 主流程 ----

MODE=""
MODE_SOURCE=""
RESULT=""         # gpu / cpu / off
RESULT_REASON=""
FIX_HINT=""       # 退回 CPU 时，改用 GPU 需要做的事
VARIANT=""
ENV_WRITTEN=""

resolve_mode() {
	local from_env
	if [ -n "$MODE_ARG" ]; then
		MODE="$MODE_ARG"
		MODE_SOURCE="命令行参数"
		return 0
	fi
	if [ -n "${TM_TRAINER_MODE:-}" ]; then
		MODE="$TM_TRAINER_MODE"
		MODE_SOURCE="环境变量 TM_TRAINER_MODE"
	elif from_env="$(env_get TM_TRAINER_MODE "$ENV_SOURCE")" && [ -n "$from_env" ]; then
		MODE="$from_env"
		MODE_SOURCE=".env 中的 TM_TRAINER_MODE"
	else
		MODE=auto
		MODE_SOURCE="默认"
		return 0
	fi
	case "$MODE" in
	auto | gpu | cpu | off) ;;
	*)
		warn "$MODE_SOURCE=$MODE 无效（应为 auto、gpu、cpu 或 off），按 auto 处理"
		MODE=auto
		MODE_SOURCE="默认"
		;;
	esac
}

# 自动模式下退回 CPU 的原因与改用 GPU 的方法。
explain_cpu_fallback() {
	if [ "$NVSMI_STATE" = missing ] && [ "$PCI_GPU_COUNT" -gt 0 ]; then
		RESULT_REASON="检测到 NVIDIA 显卡，但没有安装 NVIDIA 驱动"
		FIX_HINT="安装 NVIDIA 驱动并重启机器后，重新运行 make setup"
	elif [ "$NVSMI_STATE" = missing ] || [ "$NVSMI_STATE" = no_devices ]; then
		RESULT_REASON="未检测到 NVIDIA 显卡"
	elif [ "$NVSMI_STATE" = error ]; then
		RESULT_REASON="nvidia-smi 运行失败（$NVSMI_ERROR）"
		FIX_HINT="修复驱动问题（版本不匹配时重启机器）后，重新运行 make setup"
	elif [ "$TM_VARIANT" = cpu ]; then
		RESULT_REASON="$TM_VARIANT_REASON"
		FIX_HINT="升级 NVIDIA 驱动（推荐 580 或更新）后，重新运行 make setup"
	elif [ "$DOCKER_STATE" = missing ]; then
		RESULT_REASON="未安装 Docker"
	elif [ "$DOCKER_STATE" = unreachable ]; then
		RESULT_REASON="无法连接 Docker（$DOCKER_ERROR），无法检查它能否使用显卡"
		FIX_HINT="解决 Docker 的连接问题后，重新运行 make setup"
	elif [ "$DOCKER_GPU" = failed ]; then
		RESULT_REASON="在 Docker 中验证显卡失败（$DOCKER_GPU_DETAIL）"
		FIX_HINT="解决上面的问题后，重新运行 make setup；确认 Docker 能使用显卡时也可以用 make setup SETUP_ARGS=\"--mode gpu\" 强制使用 GPU"
	elif [ "$DOCKER_GPU" = unverified ]; then
		RESULT_REASON="Docker 中没有 NVIDIA 的配置，也无法实际验证（$DOCKER_GPU_DETAIL）"
		FIX_HINT="安装 NVIDIA Container Toolkit（make setup SETUP_ARGS=--install-toolkit）；确认 Docker 能使用显卡时也可以用 make setup SETUP_ARGS=\"--mode gpu\" 强制使用 GPU"
	elif [ "$DOCKER_GPU" = untested ]; then
		RESULT_REASON="Docker 中没有 NVIDIA 的配置（nvidia 运行时、CDI 或 NVIDIA Container Toolkit）"
		FIX_HINT="安装 NVIDIA Container Toolkit：make setup SETUP_ARGS=--install-toolkit"
	elif [ "$DOCKER_DESKTOP" = 1 ]; then
		RESULT_REASON="Docker Desktop 无法使用显卡（$DOCKER_GPU_DETAIL）"
		FIX_HINT="Docker Desktop 的 GPU 支持需要 WSL 2 后端与 Windows 上较新的 NVIDIA 驱动：更新 NVIDIA 驱动、WSL（wsl --update）与 Docker Desktop 后，重新运行 make setup"
	else
		RESULT_REASON="Docker 无法使用显卡（没有安装或配置 NVIDIA Container Toolkit）"
		FIX_HINT="安装 NVIDIA Container Toolkit：make setup SETUP_ARGS=--install-toolkit"
	fi
}

decide() {
	case "$MODE" in
	off)
		RESULT=off
		RESULT_REASON="训练节点已关闭（模式 off）"
		;;
	cpu)
		RESULT=cpu
		RESULT_REASON="按设置使用 CPU（模式 cpu）"
		;;
	gpu)
		RESULT=gpu
		RESULT_REASON="按设置使用 GPU（模式 gpu）"
		if ! host_gpu_ok; then
			warn "没有检测到可用的 NVIDIA 显卡与驱动，仍按要求使用 GPU 模式；训练节点很可能无法使用显卡"
		elif [ "$DOCKER_STATE" = ok ] && ! docker_gpu_usable; then
			warn "Docker 无法使用显卡（见上文），仍按要求使用 GPU 模式；trainer 服务可能无法启动"
		elif [ "$DOCKER_STATE" != ok ]; then
			warn "无法检查 Docker 能否使用显卡，仍按要求使用 GPU 模式"
		fi
		;;
	auto)
		if host_gpu_ok && [ "$TM_VARIANT" != cpu ] && [ "$DOCKER_STATE" = ok ] && docker_gpu_usable; then
			RESULT=gpu
			RESULT_REASON="显卡、驱动与 Docker 的 GPU 支持都正常"
			case "$DOCKER_GPU" in
			unverified) warn "没能实际验证 Docker 能否使用显卡（$DOCKER_GPU_DETAIL），按 Docker 的配置使用 GPU 模式；启动后在“计算资源”页面确认" ;;
			untested) warn "没有实际验证 Docker 能否使用显卡（--no-docker-test），按 Docker 的配置使用 GPU 模式" ;;
			esac
		else
			RESULT=cpu
			explain_cpu_fallback
		fi
		;;
	esac

	case "$RESULT" in
	cpu) VARIANT=cpu ;;
	gpu)
		local existing first
		if [ -n "$VARIANT_ARG" ]; then
			VARIANT="$VARIANT_ARG"
			if [ "$VARIANT" != "$TM_VARIANT" ]; then
				say "按 --variant 使用 PyTorch 变体 $VARIANT（按驱动与显卡推荐 $TM_VARIANT）"
			fi
			local min
			min="$(tm_variant_field "$VARIANT" min_driver)"
			if [ -n "$DRIVER" ] && ! tm_version_ge "$DRIVER" "$min"; then
				warn "$VARIANT 需要驱动 ≥ $min，当前驱动 $DRIVER"
			fi
		elif [ "$TM_VARIANT" != cpu ]; then
			VARIANT="$TM_VARIANT"
		else
			# 强制 GPU、但按驱动与显卡选不出 CUDA 版本：沿用 .env 中已有的 CUDA 变体，没有时用表中的第一个。
			existing="$(env_get TM_TORCH_VARIANT "$ENV_SOURCE" || true)"
			# 管道中不用 head：它提前退出时前面的命令会收到 SIGPIPE，在 pipefail 下导致脚本退出。
			first="$(tm_variant_names | grep -vx cpu | sed -n 1p)"
			if [ -n "$existing" ] && [ "$existing" != cpu ] && tm_variant_field "$existing" cuda >/dev/null 2>&1; then
				VARIANT="$existing"
			else
				VARIANT="$first"
			fi
			warn "按驱动与显卡无法选择 CUDA 版的 PyTorch（$TM_VARIANT_REASON），使用 $VARIANT；可用 --variant 指定"
		fi
		if [ -n "$TM_VARIANT_UNSUPPORTED" ] && [ "$VARIANT" = "$TM_VARIANT" ]; then
			warn "计算能力为 $(tm_cc_list "$TM_VARIANT_UNSUPPORTED") 的显卡不被 $VARIANT 支持，无法用于训练"
		fi
		;;
	esac
}

print_changes() {
	local key existed old new
	while IFS="$US" read -r key existed old new; do
		[ -n "$key" ] || continue
		if [ "$existed" = 1 ]; then
			if [ -n "$old" ]; then
				say "  $key=$(display_value "$key" "$new")（原为 $(display_value "$key" "$old")）"
			else
				say "  $key=$(display_value "$key" "$new")（原为空）"
			fi
		else
			say "  $key=$(display_value "$key" "$new")（新增）"
		fi
	done <<EOF
$CHANGES
EOF
}

main() {
	[ -f "$VARIANT_LIB" ] || fail "找不到 $VARIANT_LIB（请在完整的 trainMaster 仓库中运行）"
	# shellcheck source=SCRIPTDIR/../../trainer/scripts/lib/torch-variant.sh
	. "$VARIANT_LIB"
	parse_args "$@"
	validate_args
	TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/trainmaster-setup.XXXXXX")"

	# 1. .env 不存在时从 .env.example 复制
	local created=0
	if [ -e "$ENV_FILE" ]; then
		[ -f "$ENV_FILE" ] || fail "$ENV_FILE 不是普通文件"
		[ -r "$ENV_FILE" ] || fail "无法读取 $ENV_FILE"
		ENV_SOURCE="$ENV_FILE"
	elif [ "$DRY_RUN" = 1 ]; then
		if [ -f "$EXAMPLE_FILE" ]; then ENV_SOURCE="$EXAMPLE_FILE"; fi
	else
		[ -d "$(dirname "$ENV_FILE")" ] || fail "目录 $(dirname "$ENV_FILE") 不存在"
		if [ -f "$EXAMPLE_FILE" ]; then
			cp "$EXAMPLE_FILE" "$ENV_FILE" || fail "无法创建 $ENV_FILE"
		else
			: >"$ENV_FILE" || fail "无法创建 $ENV_FILE"
		fi
		created=1
		ENV_SOURCE="$ENV_FILE"
	fi

	# 2. 模式：命令行 > 环境变量 > .env > auto
	resolve_mode

	PYTHON_IMAGE="$(conf_value TRAINER_PYTHON_IMAGE "$DEFAULT_PYTHON_IMAGE")"
	TOOLKIT_REPO="$(strip_trailing_slashes "$(conf_value NVIDIA_TOOLKIT_REPO "$DEFAULT_TOOLKIT_REPO")")"
	valid_url "$TOOLKIT_REPO" || config_error "NVIDIA_TOOLKIT_REPO 必须是 http:// 或 https:// 开头的地址（当前为 $TOOLKIT_REPO）"

	say "== trainMaster 计算节点配置 =="
	if [ "$created" = 1 ]; then
		say "已从 .env.example 创建 $ENV_FILE"
	elif [ ! -e "$ENV_FILE" ]; then
		say "（--dry-run）$ENV_FILE 不存在，实际运行时会从 .env.example 复制"
	fi

	# 3. 显卡与驱动；6. 选择 PyTorch 变体
	detect_gpus
	report_gpus
	tm_select_variant "$DRIVER" "$GPU_CCS" "$MACHINE"
	if host_gpu_ok; then
		say "推荐的 PyTorch 变体：$TM_VARIANT（$TM_VARIANT_REASON）"
	fi

	# 4. Docker 与它的 GPU 支持；5. 需要时安装 NVIDIA Container Toolkit
	detect_docker
	report_docker_state
	if [ "$DOCKER_STATE" = ok ] && host_gpu_ok && { [ "$MODE" = gpu ] || { [ "$MODE" = auto ] && [ "$TM_VARIANT" != cpu ]; }; }; then
		if [ "$DOCKER_TEST" = 1 ]; then
			docker_gpu_test
		else
			DOCKER_GPU=untested
		fi
		report_docker_gpu
		if toolkit_needed; then
			if [ "$INSTALL_TOOLKIT" = 1 ]; then
				local rc=0
				install_toolkit || rc=$?
				if [ "$rc" -eq 1 ]; then
					fail "NVIDIA Container Toolkit 安装失败，没有修改 .env。解决上面的问题后重新运行，或参考 $TOOLKIT_DOC_URL 手动安装"
				fi
				if [ "$rc" -eq 0 ]; then
					say "重新检查 Docker 的 GPU 支持…"
					wait_for_docker || warn "Docker 在 $DOCKER_WAIT 秒内没有恢复响应"
					detect_docker
					report_docker_state
					if [ "$DOCKER_STATE" = ok ]; then
						if [ "$DOCKER_TEST" = 1 ]; then docker_gpu_test; else DOCKER_GPU=untested; fi
						report_docker_gpu
					fi
				fi
			else
				print_toolkit_hint
			fi
		elif [ "$INSTALL_TOOLKIT" = 1 ] && docker_gpu_usable; then
			say "Docker 已经可以使用显卡，不需要安装 NVIDIA Container Toolkit"
		elif [ "$INSTALL_TOOLKIT" = 1 ] && [ "$DOCKER_DESKTOP" = 1 ]; then
			say "Docker Desktop 自带 GPU 支持，不需要（也不能）安装 NVIDIA Container Toolkit"
		elif [ "$INSTALL_TOOLKIT" = 1 ]; then
			say "Docker 无法使用显卡的原因不是缺少 NVIDIA Container Toolkit（见上文），不安装"
		fi
	elif [ "$INSTALL_TOOLKIT" = 1 ]; then
		if ! host_gpu_ok; then
			say "没有可用的 NVIDIA 显卡与驱动，不安装 NVIDIA Container Toolkit"
		elif [ "$DOCKER_STATE" != ok ]; then
			say "Docker 不可用，不安装 NVIDIA Container Toolkit"
		else
			say "当前模式（$MODE）不使用 GPU，不安装 NVIDIA Container Toolkit"
		fi
	fi

	# 7. 决定模式与变体
	decide

	# 8. 写入 .env
	local old_profiles new_profiles server_url node_name token remote=0
	old_profiles="$(env_get COMPOSE_PROFILES "$ENV_SOURCE" || true)"
	new_profiles="$(merge_profiles "$old_profiles" "$RESULT")"
	if [ -n "$MODE_ARG" ]; then set_key TM_TRAINER_MODE "$MODE_ARG"; fi
	set_key COMPOSE_PROFILES "$new_profiles"
	if [ "$RESULT" != off ]; then
		set_key TM_TORCH_VARIANT "$VARIANT"
		node_name="$(env_get TM_NODE_NAME "$ENV_SOURCE" || true)"
		if [ -z "$node_name" ]; then
			node_name="$(node_hostname)"
			if [ -n "$node_name" ]; then set_key TM_NODE_NAME "$node_name"; fi
		fi
	fi
	if [ -n "$SERVER_URL_ARG" ]; then
		set_key TM_SERVER_URL "$SERVER_URL_ARG"
		server_url="$SERVER_URL_ARG"
	else
		server_url="$(env_get TM_SERVER_URL "$ENV_SOURCE" || true)"
	fi
	if [ -n "$server_url" ] && [ "$server_url" != "$DEFAULT_SERVER_URL" ]; then
		remote=1
	fi
	token="$(env_get TM_NODE_TOKEN "$ENV_SOURCE" || true)"
	if [ -n "$TOKEN_ARG" ]; then
		set_key TM_NODE_TOKEN "$TOKEN_ARG"
	elif [ -z "$token" ] && [ "$remote" = 0 ]; then
		# 平台与本机训练节点共用 .env 中的令牌；其它机器上的节点需要使用平台的令牌，不能自己生成。
		set_key TM_NODE_TOKEN "$(generate_token)"
		TOKEN_GENERATED=1
	fi
	write_env

	# docker compose 优先使用 shell 中的环境变量，其次才是 .env：与 .env 中的结果不同时提醒。
	local key var final
	for key in COMPOSE_PROFILES TM_TORCH_VARIANT TM_NODE_NAME TM_NODE_TOKEN TM_SERVER_URL; do
		[ -n "${!key+x}" ] || continue
		var="NEW_$key"
		if [ -n "${!var+x}" ]; then
			final="${!var}"
		else
			final="$(env_get "$key" "$ENV_SOURCE" || true)"
		fi
		if [ "${!key}" != "$final" ]; then
			warn "环境变量 $key 已设置为“$(display_value "$key" "${!key}")”，docker compose 会优先使用它而不是 .env 中的值"
		fi
	done

	# 9. 摘要
	say ""
	say "结果："
	case "$RESULT" in
	gpu) say "  训练节点：GPU（trainer 服务，PyTorch 变体 $VARIANT）" ;;
	cpu) say "  训练节点：CPU（trainer-cpu 服务，PyTorch 变体 cpu）" ;;
	off) say "  训练节点：不运行" ;;
	esac
	say "  模式：$MODE（$MODE_SOURCE）"
	say "  原因：$RESULT_REASON"
	if [ "$RESULT" = cpu ] && [ "$MODE" = auto ] && [ -n "$FIX_HINT" ]; then
		say "  改用 GPU：$FIX_HINT"
	fi
	case "$ENV_WRITTEN" in
	unchanged) say ".env 无需修改（$ENV_FILE）" ;;
	dry-run)
		say "将写入 $ENV_FILE（--dry-run，未修改）："
		print_changes
		;;
	written)
		say "已更新 $ENV_FILE："
		print_changes
		;;
	esac
	if [ "${TOKEN_GENERATED:-0}" = 1 ]; then
		if [ "$DRY_RUN" = 1 ]; then
			say "  将生成节点令牌 TM_NODE_TOKEN（实际运行时写入 .env）"
		else
			say "  已生成节点令牌 TM_NODE_TOKEN（查看：grep TM_NODE_TOKEN .env）。其它机器上的训练节点需要使用同一个令牌"
		fi
	fi
	if [ "$remote" = 1 ] && [ -z "$TOKEN_ARG" ] && [ -z "$token" ] && [ "$RESULT" != off ]; then
		warn "这台机器上的训练节点连接 $server_url，但没有设置 TM_NODE_TOKEN：平台设置了令牌时节点无法注册。把平台 .env 中的 TM_NODE_TOKEN 填入 .env，或重新运行并加 --token <令牌>"
	fi

	if [ "$ENV_WRITTEN" = written ]; then
		remove_stale_trainers "$old_profiles" "$new_profiles"
	fi

	say ""
	local svc=trainer
	if [ "$RESULT" = cpu ]; then svc=trainer-cpu; fi
	if [ "${TM_SETUP_CALLER:-}" = up ]; then
		if [ "$remote" = 1 ] && [ "$RESULT" != off ]; then
			warn "这台机器配置为连接其它平台的训练节点（TM_SERVER_URL=$server_url），make up 会同时启动本机的平台服务；只运行训练节点请用 docker compose up -d --build $svc"
		fi
		say "接下来构建并启动服务（docker compose up -d --build）…"
		return 0
	fi
	say "下一步："
	if [ "$DRY_RUN" = 1 ]; then
		say "  去掉 --dry-run 重新运行以写入 .env"
	elif [ "$remote" = 1 ] && [ "$RESULT" != off ]; then
		say "  docker compose up -d --build $svc    # 只启动训练节点，连接 $server_url"
	else
		say "  make up    # 或 docker compose up -d --build"
	fi
	if [ "$RESULT" != off ] && [ "$DRY_RUN" = 0 ]; then
		say "  首次构建训练节点镜像需要下载 PyTorch、Ultralytics 等依赖（GPU 版有数 GB），国内网络请先在 .env 中配置镜像（见 docs/deployment.md）"
		say "  启动后在平台的“模型训练 → 计算资源”页面查看节点、显卡与 CUDA 环境"
	fi
	return 0
}

main "$@"
