#!/usr/bin/env bash
# deploy/scripts/setup-compute.sh 的测试：bash deploy/scripts/tests/test-setup-compute.sh
#
# 每个用例在临时目录中运行脚本的副本：nvidia-smi、docker、id、sudo、curl、gpg、tee、包管理器、
# nvidia-ctk、systemctl、service、hostname 都是记录调用的替身脚本，PATH 中只有这些替身和白名单中的
# 基础工具（awk、sed 等）；systemd 是否在运行、是不是 WSL 也由用例目录中的文件决定。因此测试不需要
# Docker 或显卡，也不会读取或修改真实系统的配置。
# FAKE_* 变量经 run_setup（compgen）传给替身命令；单引号中的 $ 属于替身脚本或期望的文件内容；中文引号是有意的。
# shellcheck disable=SC2034,SC2016,SC1111
set -euo pipefail

TESTS_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$TESTS_DIR/../../.." && pwd)"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-setup-compute.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

fails=0
checks=0
CASE_NAME=""

fail() {
	echo "FAIL [$CASE_NAME] $*" >&2
	fails=$((fails + 1))
}

# ---- 沙箱：脚本副本、白名单工具与替身命令 ----

REPO="$WORK/repo"
mkdir -p "$REPO/deploy/scripts" "$REPO/trainer/scripts/lib" "$REPO/trainer/trainmaster_trainer"
cp "$REPO_ROOT/deploy/scripts/setup-compute.sh" "$REPO/deploy/scripts/"
cp "$REPO_ROOT/trainer/scripts/lib/torch-variant.sh" "$REPO/trainer/scripts/lib/"
cp "$REPO_ROOT/trainer/trainmaster_trainer/torch_variants.txt" "$REPO/trainer/trainmaster_trainer/"
cp "$REPO_ROOT/.env.example" "$REPO/.env.example"
SCRIPT="$REPO/deploy/scripts/setup-compute.sh"

SYSBIN="$WORK/sysbin"
mkdir -p "$SYSBIN"
for tool in bash awk sed grep cat cut tr head tail wc mktemp mv cp rm dirname basename uname base64 cmp sleep timeout touch; do
	path="$(command -v "$tool" || true)"
	if [ -z "$path" ]; then
		echo "缺少运行测试所需的命令：$tool" >&2
		exit 1
	fi
	ln -s "$path" "$SYSBIN/$tool"
done
# 没有 timeout 命令的系统（如 macOS）：脚本改用后台进程计时
SYSBIN_NO_TIMEOUT="$WORK/sysbin-no-timeout"
mkdir -p "$SYSBIN_NO_TIMEOUT"
for f in "$SYSBIN"/*; do
	[ "$(basename "$f")" = timeout ] || ln -s "$(readlink "$f")" "$SYSBIN_NO_TIMEOUT/"
done

BIN="$WORK/bin"
# pkgs：包管理器“安装”软件包时复制到用例的 installed 目录（在 PATH 中）的替身
mkdir -p "$BIN" "$WORK/pm-apt" "$WORK/pm-dnf" "$WORK/pm-yum" "$WORK/pm-zypper" "$WORK/net" "$WORK/hook" "$WORK/pkgs"

# fake <路径>：从标准输入读取替身脚本的主体，加上 shebang 与记录调用的 log 函数。
fake() {
	{
		printf '#!%s\n' "$BASH"
		printf '%s\n' 'log() { printf "%s\n" "$*" >>"$FAKE_LOG"; }'
		cat
	} >"$1"
	chmod +x "$1"
}

fake "$BIN/nvidia-smi" <<'EOF'
log "nvidia-smi $*"
case "${FAKE_SMI_MODE:-ok}" in
mismatch)
	echo "Failed to initialize NVML: Driver/library version mismatch"
	echo "NVML library version: 580.82"
	exit 18
	;;
error)
	echo "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver. Make sure that the latest NVIDIA driver is installed and running."
	exit 9
	;;
nodevices)
	echo "No devices were found"
	exit 6
	;;
esac
with_cc=0
case "$*" in *compute_cap*) with_cc=1 ;; esac
if [ "$with_cc" = 1 ] && [ "${FAKE_SMI_MODE:-ok}" = old ]; then
	echo 'Field "compute_cap" is not a valid field to query.'
	echo
	echo "Run 'nvidia-smi --help' for usage."
	exit 2
fi
# FAKE_GPUS：每行一块显卡，名称|驱动|计算能力|显存（MiB）
while IFS='|' read -r name driver cc mem; do
	[ -n "$name" ] || continue
	if [ "$with_cc" = 1 ]; then
		printf '%s, %s, %s, %s MiB\r\n' "$name" "$driver" "$cc" "$mem"
	else
		printf '%s, %s, %s MiB\n' "$name" "$driver" "$mem"
	fi
done <<GPUS
${FAKE_GPUS:-}
GPUS
EOF

fake "$BIN/docker" <<'EOF'
log "docker $*"
state="$FAKE_STATE"
case "$1" in
info)
	case "${FAKE_DOCKER_INFO:-ok}" in
	denied)
		echo 'permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Get "http://%2Fvar%2Frun%2Fdocker.sock/v1.47/info": dial unix /var/run/docker.sock: connect: permission denied' >&2
		exit 1
		;;
	down)
		echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2
		exit 1
		;;
	esac
	# 真实的 docker info 会在 stderr 打印警告，不能影响解析
	echo "WARNING: bridge-nf-call-iptables is disabled" >&2
	runtimes="io.containerd.runc.v2 runc "
	if [ -e "$state/toolkit" ]; then runtimes="io.containerd.runc.v2 nvidia runc "; fi
	if [ -n "${FAKE_DOCKER_RUNTIMES:-}" ]; then runtimes="$FAKE_DOCKER_RUNTIMES"; fi
	case "$*" in
	*OperatingSystem*)
		# FAKE_DOCKER_ROOT：数据目录（snap 版为 /var/snap/docker/…）；FAKE_DOCKER_SECURITY：SecurityOptions
		printf '%s|%s|%s|%s\n' "${FAKE_DOCKER_OS:-Ubuntu 22.04.4 LTS}" "$runtimes" \
			"${FAKE_DOCKER_ROOT:-/var/lib/docker}" "${FAKE_DOCKER_SECURITY:-[name=apparmor name=seccomp,profile=builtin name=cgroupns]}"
		;;
	*) echo "Server Version: 28.5.1" ;;
	esac
	;;
image)
	if [ "${FAKE_DOCKER_IMAGE:-present}" != present ] && [ ! -e "$state/pulled" ]; then
		echo "Error: No such image: ${*: -1}" >&2
		exit 1
	fi
	echo '[{}]'
	;;
pull)
	if [ "${FAKE_DOCKER_IMAGE:-present}" = offline ]; then
		echo 'Error response from daemon: Get "https://registry-1.docker.io/v2/": dial tcp: lookup registry-1.docker.io: no such host' >&2
		exit 1
	fi
	touch "$state/pulled"
	echo "docker.io/library/${*: -1}"
	;;
run)
	gpu="${FAKE_DOCKER_GPU:-auto}"
	if [ "$gpu" = auto ]; then
		if [ -e "$state/toolkit" ]; then gpu=ok; else gpu=nodriver; fi
	fi
	case "$gpu" in
	ok)
		i=0
		while [ "$i" -lt "${FAKE_CONTAINER_GPUS:-1}" ]; do
			echo "GPU $i: NVIDIA GeForce RTX 4090 (UUID: GPU-0000000$i)"
			i=$((i + 1))
		done
		;;
	nodriver)
		echo 'docker: Error response from daemon: could not select device driver "nvidia" with capabilities: [[gpu]].' >&2
		echo "Run 'docker run --help' for more information" >&2
		exit 125
		;;
	nocdi)
		# Docker 28 之后经 CDI 实现 --gpus，没有 NVIDIA Container Toolkit 时的报错
		echo 'docker: Error response from daemon: failed to discover GPU vendor from CDI: no known GPU vendor found' >&2
		exit 125
		;;
	amdcdi)
		# Docker 29.5 起：安装了 Toolkit（有 NVIDIA 的 CDI 配置）但没有重启 Docker 时的报错；
		# nvidia-ctk runtime configure 之后（state/toolkit）视为已重启
		if [ -e "$state/toolkit" ]; then
			echo "GPU 0: NVIDIA GeForce RTX 4090 (UUID: GPU-00000000)"
			exit 0
		fi
		echo 'docker: Error response from daemon: AMD CDI spec not found' >&2
		exit 125
		;;
	mismatch)
		echo 'docker: Error response from daemon: failed to create task for container: failed to create shim task: OCI runtime create failed: nvidia-container-cli: initialization error: nvml error: driver/library version mismatch: unknown.' >&2
		exit 125
		;;
	hang) exec sleep 5 ;;
	esac
	;;
rm) ;;
compose)
	svc="${*: -1}"
	case " $* " in
	*" ps -aq "*) if [ -e "$state/container-$svc" ]; then echo "0123456789ab"; fi ;;
	*" rm "*) rm -f "$state/container-$svc" ;;
	esac
	;;
esac
exit 0
EOF

fake "$BIN/id" <<'EOF'
if [ "${1:-}" = -u ]; then echo "${FAKE_UID:-1000}"; else echo "uid=${FAKE_UID:-1000}"; fi
EOF
fake "$BIN/sudo" <<'EOF'
log "sudo $*"
exec "$@"
EOF
fake "$BIN/hostname" <<'EOF'
echo "${FAKE_HOSTNAME:-gpu-host-01}"
EOF
fake "$BIN/tee" <<'EOF'
log "tee $*"
content="$(cat)"
printf '%s\n' "$content" >"$FAKE_STATE/tee-$(basename "${*: -1}")"
printf '%s\n' "$content"
EOF
fake "$WORK/pkgs/nvidia-ctk" <<'EOF'
log "nvidia-ctk $*"
touch "$FAKE_STATE/toolkit"
EOF
fake "$BIN/systemctl" <<'EOF'
log "systemctl $*"
EOF
fake "$BIN/service" <<'EOF'
log "service $*"
EOF
fake "$WORK/net/curl" <<'EOF'
log "curl $*"
if [ -n "${FAKE_CURL_FAIL:-}" ]; then
	echo "curl: (6) Could not resolve host" >&2
	exit 6
fi
case "${*: -1}" in
*/gpgkey) echo "-----BEGIN PGP PUBLIC KEY BLOCK-----" ;;
*.list)
	echo 'deb https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /'
	echo '#deb https://nvidia.github.io/libnvidia-container/experimental/deb/$(ARCH) /'
	;;
*.repo)
	echo '[nvidia-container-toolkit]'
	echo 'baseurl=https://nvidia.github.io/libnvidia-container/stable/rpm/$basearch'
	echo 'gpgkey=https://nvidia.github.io/libnvidia-container/gpgkey'
	;;
esac
EOF
fake "$WORK/net/gpg" <<'EOF'
log "gpg $*"
cat >/dev/null
EOF
for pm in apt-get dnf yum zypper; do
	case "$pm" in
	apt-get) dir="$WORK/pm-apt" ;;
	dnf) dir="$WORK/pm-dnf" ;;
	yum) dir="$WORK/pm-yum" ;;
	zypper) dir="$WORK/pm-zypper" ;;
	esac
	fake "$dir/$pm" <<EOF
log "$pm \$*"
case " \$* " in
*" lr "*) exit 6 ;;
*" install "*)
	if [ -n "\${FAKE_PM_FAIL:-}" ]; then
		echo "E: Unable to locate package nvidia-container-toolkit" >&2
		exit 100
	fi
	# 把装好的软件包的替身放进 PATH 中的目录
	for pkg in \$*; do
		case "\$pkg" in
		curl) cp "\$FAKE_PKG_DIR/curl" "\$FAKE_INSTALL_DIR/" ;;
		gnupg2) cp "\$FAKE_PKG_DIR/gpg" "\$FAKE_INSTALL_DIR/" ;;
		nvidia-container-toolkit) cp "\$FAKE_PKG_DIR/nvidia-ctk" "\$FAKE_PKG_DIR/nvidia-container-runtime-hook" "\$FAKE_INSTALL_DIR/" ;;
		esac
	done
	;;
esac
exit 0
EOF
done
fake "$WORK/hook/nvidia-container-runtime-hook" <<'EOF'
exit 0
EOF
cp "$WORK/hook/nvidia-container-runtime-hook" "$WORK/net/curl" "$WORK/net/gpg" "$WORK/pkgs/"

# ---- 用例框架 ----

GPU_4090="NVIDIA GeForce RTX 4090|550.54.15|8.9|24564"

# new_case <名称>：新的空目录与默认设置（一块 RTX 4090、驱动 550、Docker 正常但没有 NVIDIA Container Toolkit，
# systemd 正在运行，不是 WSL）
new_case() {
	CASE_NAME="$1"
	CASE="$WORK/cases/$(printf '%s' "$1" | tr -c 'A-Za-z0-9_-' '_')"
	mkdir -p "$CASE/state" "$CASE/tmp" "$CASE/sys/bus/pci/devices" "$CASE/cdi" "$CASE/run-systemd"
	echo "6.8.0-45-generic" >"$CASE/osrelease"
	ENV="$CASE/.env"
	local v
	for v in $(compgen -v FAKE_); do unset "$v"; done
	FAKE_LOG="$CASE/log"
	FAKE_STATE="$CASE/state"
	FAKE_GPUS="$GPU_4090"
	: >"$FAKE_LOG"
	SMI="$BIN/nvidia-smi"
	EXTRA_PATH="$WORK/net:$WORK/pm-apt"
	SYS="$SYSBIN"
	FAKEBIN="$BIN"
	mkdir -p "$CASE/installed"
	FAKE_INSTALL_DIR="$CASE/installed"
	FAKE_PKG_DIR="$WORK/pkgs"
	STDIN_FILE=/dev/null
	EXTRA_ENV=()
}

# run_setup [参数…]：运行脚本，合并的输出在 $OUT，退出码在 $RC。环境只包含明确传入的变量。
run_setup() {
	local -a vars=()
	local v
	for v in $(compgen -v FAKE_); do vars+=("$v=${!v}"); done
	RC=0
	OUT="$(cd "$CASE" && env -i \
		PATH="${EXTRA_PATH:+$EXTRA_PATH:}$CASE/installed:$FAKEBIN:$SYS" HOME="$CASE" TMPDIR="$CASE/tmp" LANG=C.UTF-8 \
		TM_NVIDIA_SMI="$SMI" TM_DOCKER="$BIN/docker" TM_SYSFS_ROOT="$CASE/sys" \
		TM_CDI_SPECS="$CASE/cdi/nvidia.yaml" TM_MACHINE=x86_64 TM_DOCKER_WAIT=2 \
		TM_SYSTEMD_DIR="$CASE/run-systemd" TM_OSRELEASE_FILE="$CASE/osrelease" \
		"${vars[@]}" ${EXTRA_ENV[@]+"${EXTRA_ENV[@]}"} \
		"$BASH" "$SCRIPT" --env-file "$ENV" "$@" 2>&1 <"$STDIN_FILE")" || RC=$?
	if [ -n "${VERBOSE:-}" ]; then
		printf -- '---- %s：setup-compute.sh %s（退出码 %s）\n%s\n' "$CASE_NAME" "$*" "$RC" "$OUT"
	fi
}

# pci_gpu <设备名> <厂商> <类别>：在假的 sysfs 中添加一个 PCI 设备
pci_device() {
	mkdir -p "$CASE/sys/bus/pci/devices/$1"
	echo "$2" >"$CASE/sys/bus/pci/devices/$1/vendor"
	echo "$3" >"$CASE/sys/bus/pci/devices/$1/class"
}

# env_value <KEY>：.env 中最后一个未注释的 KEY= 的值（不存在时返回 1）
env_value() {
	awk -v k="$1" 'index($0, k "=") == 1 { v = substr($0, length(k) + 2); sub(/\r$/, "", v); found = 1 } END { if (found) print v; exit !found }' "$ENV"
}

expect_rc() {
	checks=$((checks + 1))
	[ "$RC" = "$1" ] || fail "退出码应为 $1，实际为 $RC；输出：
$OUT"
}

expect_env() { # expect_env <KEY> <值>
	checks=$((checks + 1))
	local got
	if ! got="$(env_value "$1")"; then
		fail ".env 中没有 $1（应为 [$2]）"
	elif [ "$got" != "$2" ]; then
		fail ".env 中 $1 应为 [$2]，实际为 [$got]"
	fi
}

expect_no_env() { # expect_no_env <KEY>
	checks=$((checks + 1))
	if env_value "$1" >/dev/null 2>&1; then
		fail ".env 中不应有 $1=（实际为 [$(env_value "$1")]）"
	fi
}

expect_out() { # expect_out <子串>
	checks=$((checks + 1))
	case "$OUT" in
	*"$1"*) ;;
	*) fail "输出中应包含 [$1]；输出：
$OUT" ;;
	esac
}

expect_no_out() {
	checks=$((checks + 1))
	case "$OUT" in
	*"$1"*) fail "输出中不应包含 [$1]；输出：
$OUT" ;;
	esac
}

expect_log() { # expect_log <子串>：替身命令的调用记录中有这一项
	checks=$((checks + 1))
	grep -qF -- "$1" "$FAKE_LOG" || fail "应调用 [$1]；调用记录：
$(cat "$FAKE_LOG")"
}

expect_no_log() {
	checks=$((checks + 1))
	if grep -qF -- "$1" "$FAKE_LOG"; then
		fail "不应调用 [$1]；调用记录：
$(cat "$FAKE_LOG")"
	fi
}

expect_same() { # expect_same <名称> <期望> <实际>
	checks=$((checks + 1))
	[ "$2" = "$3" ] || fail "$1：期望 [$2]，实际 [$3]"
}

# 调用记录中各项的先后顺序
expect_log_order() {
	checks=$((checks + 1))
	local prev=0 n item
	for item in "$@"; do
		n="$(grep -nF -m 1 -- "$item" "$FAKE_LOG" | cut -d: -f1)"
		if [ -z "$n" ] || [ "$n" -le "$prev" ]; then
			fail "调用顺序不对，应依次为：$*；调用记录：
$(cat "$FAKE_LOG")"
			return
		fi
		prev="$n"
	done
}

# with_toolkit：已安装 NVIDIA Container Toolkit 并配置了 Docker
with_toolkit() {
	toolkit_installed_only
	touch "$FAKE_STATE/toolkit"
}

# toolkit_installed_only：已安装 NVIDIA Container Toolkit，但还没有配置或重启 Docker
toolkit_installed_only() {
	cp "$WORK/pkgs/nvidia-ctk" "$WORK/pkgs/nvidia-container-runtime-hook" "$CASE/installed/"
}

# expect_out_order <子串…>：输出中各项的先后顺序（安装命令在执行前逐条打印为“+ 命令”）
expect_out_order() {
	checks=$((checks + 1))
	local rest="$OUT" item
	for item in "$@"; do
		case "$rest" in
		*"$item"*) rest="${rest#*"$item"}" ;;
		*)
			fail "输出中应依次出现：$*；缺少或顺序不对：[$item]；输出：
$OUT"
			return
			;;
		esac
	done
}

# ---- 用例 ----

# 有 GPU、已安装 NVIDIA Container Toolkit：GPU 模式，.env 从 .env.example 创建并原地填写
new_case "gpu with toolkit"
with_toolkit
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126
expect_env TM_NODE_NAME gpu-host-01
expect_env TM_TRAINER_MODE auto
expect_out "检测到 1 块 NVIDIA 显卡，驱动 550.54.15"
expect_out "NVIDIA GeForce RTX 4090 · 显存 24.0 GiB · 计算能力 8.9"
expect_out "Docker GPU 支持：正常"
expect_out "训练节点：GPU（trainer 服务，PyTorch 变体 cu126）"
expect_out "已从 .env.example 创建"
expect_log "docker run --rm --name trainmaster-gpu-check-"
expect_log "--gpus all python:3.12-slim-bookworm nvidia-smi -L"
expect_no_log "docker pull"
token="$(env_value TM_NODE_TOKEN)"
checks=$((checks + 1))
[[ "$token" =~ ^[A-Za-z0-9]{32}$ ]] || fail "令牌应为 32 个字母或数字，实际为 [$token]"
expect_no_out "$token"
expect_out "TM_NODE_TOKEN=${token:0:4}…（32 个字符）"
# 键都在 .env.example 中，原地替换，不追加标题；其它内容与 .env.example 逐行一致
checks=$((checks + 1))
grep -qF -- "由 make setup / deploy/scripts/setup-compute.sh 写入" "$ENV" && fail "不应追加标题"
expect_env TM_HOST_HOSTNAME gpu-host-01
expect_same "除写入的键外与 .env.example 相同" \
	"$(grep -vE '^(COMPOSE_PROFILES|TM_TORCH_VARIANT|TM_NODE_NAME|TM_HOST_HOSTNAME|TM_NODE_TOKEN)=' "$REPO/.env.example")" \
	"$(grep -vE '^(COMPOSE_PROFILES|TM_TORCH_VARIANT|TM_NODE_NAME|TM_HOST_HOSTNAME|TM_NODE_TOKEN)=' "$ENV")"
expect_same "行数不变" "$(wc -l <"$REPO/.env.example")" "$(wc -l <"$ENV")"

# 再运行一次：.env 不变，令牌不重新生成
before="$(cat "$ENV")"
run_setup
expect_rc 0
expect_out ".env 无需修改"
expect_same "第二次运行后 .env 不变" "$before" "$(cat "$ENV")"
expect_env TM_NODE_TOKEN "$token"

# 驱动 ≥ 580：CUDA 13（cu130）；多块显卡
new_case "driver 580 multi gpu"
with_toolkit
FAKE_GPUS="NVIDIA H100 80GB HBM3|580.65.06|9.0|81559
NVIDIA A100-SXM4-80GB|580.65.06|8.0|81920"
FAKE_CONTAINER_GPUS=2
run_setup
expect_rc 0
expect_env TM_TORCH_VARIANT cu130
expect_env COMPOSE_PROFILES gpu
expect_out "检测到 2 块 NVIDIA 显卡"
expect_out "GPU 1：NVIDIA A100-SXM4-80GB · 显存 80.0 GiB · 计算能力 8.0"
expect_out "看到 2 块显卡"

# 有 GPU、没有 NVIDIA Container Toolkit：退回 CPU，并给出安装命令（不执行）
new_case "gpu without toolkit"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES cpu
expect_env TM_TORCH_VARIANT cpu
expect_out "Docker GPU 支持：不可用"
expect_out "could not select device driver"
expect_out "没有安装 NVIDIA Container Toolkit"
expect_out "make setup SETUP_ARGS=--install-toolkit"
expect_out "sudo apt-get install -y nvidia-container-toolkit"
expect_out "sudo nvidia-ctk runtime configure --runtime=docker"
expect_out "sudo systemctl restart docker"
expect_out "训练节点：CPU（trainer-cpu 服务，PyTorch 变体 cpu）"
expect_out "改用 GPU：安装 NVIDIA Container Toolkit"
expect_no_log "apt-get"
expect_no_log "sudo"
expect_no_log "nvidia-ctk"

# 较新的 Docker（经 CDI 实现 --gpus）没有 Toolkit 时的报错同样识别为“不支持”
new_case "gpu without toolkit newer docker"
FAKE_DOCKER_GPU=nocdi
run_setup
expect_rc 0
expect_out "Docker GPU 支持：不可用（docker: Error response from daemon: failed to discover GPU vendor from CDI"
expect_out "make setup SETUP_ARGS=--install-toolkit"
expect_env COMPOSE_PROFILES cpu

# 已安装 NVIDIA Container Toolkit，但没有配置或重启 Docker：只提示配置与重启，不再提示安装软件包
new_case "toolkit installed but docker not configured"
toolkit_installed_only
run_setup
expect_rc 0
expect_out "已安装 NVIDIA Container Toolkit，但 Docker 还没有配置或重启"
expect_out "已安装 NVIDIA Container Toolkit，还需要配置并重启 Docker"
expect_out "sudo nvidia-ctk runtime configure --runtime=docker"
expect_out "sudo systemctl restart docker"
expect_out "改用 GPU：配置并重启 Docker：make setup SETUP_ARGS=--install-toolkit"
expect_no_out "apt-get install -y nvidia-container-toolkit"
expect_env COMPOSE_PROFILES cpu
expect_no_log "nvidia-ctk"
# --install-toolkit：只配置并重启 Docker（不安装软件包），然后改用 GPU
run_setup --install-toolkit --yes
expect_rc 0
expect_out "将执行以下命令配置 Docker 使用已安装的 NVIDIA Container Toolkit"
expect_log_order "sudo nvidia-ctk runtime configure --runtime=docker" "sudo systemctl restart docker"
expect_no_log "apt-get"
expect_no_log "curl"
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126

# Docker 29.5 起：安装 Toolkit 后没有重启 Docker 时报 AMD CDI spec not found，同样识别为“需要配置并重启”
new_case "toolkit installed docker 29 amd cdi"
toolkit_installed_only
echo "cdiVersion: 0.5.0" >"$CASE/cdi/nvidia.yaml"
FAKE_DOCKER_GPU=amdcdi
run_setup
expect_rc 0
expect_out "Docker GPU 支持：不可用（docker: Error response from daemon: AMD CDI spec not found）"
expect_out "已安装 NVIDIA Container Toolkit，但 Docker 还没有配置或重启"
expect_out "重启 Docker 后即可使用显卡"
expect_out "make setup SETUP_ARGS=--install-toolkit"
expect_no_out "验证失败"
expect_env COMPOSE_PROFILES cpu
run_setup --install-toolkit --yes
expect_rc 0
expect_no_out "原因不是缺少 NVIDIA Container Toolkit"
expect_log_order "sudo nvidia-ctk runtime configure --runtime=docker" "sudo systemctl restart docker"
expect_no_log "apt-get"
expect_out "Docker GPU 支持：正常"
expect_env COMPOSE_PROFILES gpu

# Docker 已经能使用显卡时 --install-toolkit 什么也不做，正常完成
new_case "install toolkit not needed"
with_toolkit
run_setup --install-toolkit --yes
expect_rc 0
expect_out "Docker 已经可以使用显卡，不需要安装或配置 NVIDIA Container Toolkit"
expect_no_log "apt-get"
expect_no_log "nvidia-ctk"
expect_no_log "systemctl"
expect_env COMPOSE_PROFILES gpu
# 没有显卡时同样不做任何事
new_case "install toolkit without gpu"
SMI="nvidia-smi-not-installed"
run_setup --install-toolkit --yes
expect_rc 0
expect_out "没有可用的 NVIDIA 显卡与驱动，不安装 NVIDIA Container Toolkit"
expect_no_log "apt-get"
expect_env COMPOSE_PROFILES cpu
# 驱动过旧（只能用 CPU 版 PyTorch）时不安装
new_case "install toolkit old driver"
FAKE_GPUS="Tesla T4|418.226.00|7.5|15109"
run_setup --install-toolkit --yes
expect_rc 0
expect_out "驱动不能使用 CUDA 版的 PyTorch"
expect_no_log "apt-get"
expect_env COMPOSE_PROFILES cpu

# 安装 NVIDIA Container Toolkit（apt，非 root 用 sudo，--yes）：按官方步骤执行后重新验证，改用 GPU
new_case "install toolkit apt"
FAKE_DOCKER_IMAGE=pull
run_setup --install-toolkit --yes
expect_rc 0
expect_out_order \
	"+ curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --batch --yes --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg" \
	"+ curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | sed -e 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list" \
	"+ sudo apt-get update" \
	"+ sudo apt-get install -y nvidia-container-toolkit" \
	"+ sudo nvidia-ctk runtime configure --runtime=docker" \
	"+ sudo systemctl restart docker"
for call in "curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey" \
	"sudo gpg --batch --yes --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg" \
	"curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list" \
	"sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list"; do
	expect_log "$call"
done
expect_log_order "sudo apt-get update" "sudo apt-get install -y nvidia-container-toolkit" \
	"sudo nvidia-ctk runtime configure --runtime=docker" "sudo systemctl restart docker"
expect_log "docker pull -q python:3.12-slim-bookworm"
expect_same "软件源文件加上 signed-by" \
	'deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /
#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://nvidia.github.io/libnvidia-container/experimental/deb/$(ARCH) /' \
	"$(cat "$FAKE_STATE/tee-nvidia-container-toolkit.list")"
expect_out "+ sudo apt-get install -y nvidia-container-toolkit"
expect_out "+ sudo systemctl restart docker"
expect_out "Docker GPU 支持：正常"
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126
expect_no_log "apt-get install -y --no-install-recommends curl gnupg2"

# 没有 curl / gpg 时先安装它们；询问确认，回答 y
new_case "install toolkit apt prompt yes"
EXTRA_PATH="$WORK/pm-apt"
STDIN_FILE="$CASE/answer"
echo y >"$STDIN_FILE"
run_setup --install-toolkit
expect_rc 0
expect_out "继续执行？[y/N]"
expect_log_order "sudo apt-get update" "sudo apt-get install -y --no-install-recommends curl gnupg2" "sudo nvidia-ctk runtime configure --runtime=docker"

# 询问确认，回答 n：不安装，退回 CPU
new_case "install toolkit declined"
STDIN_FILE="$CASE/answer"
echo n >"$STDIN_FILE"
run_setup --install-toolkit
expect_rc 0
expect_out "已取消安装。"
expect_no_log "apt-get"
expect_no_log "nvidia-ctk"
expect_env COMPOSE_PROFILES cpu

# 没有输入可读（非交互）也不会安装
new_case "install toolkit no input"
run_setup --install-toolkit
expect_rc 0
expect_out "已取消安装。"
expect_no_log "nvidia-ctk"

# root 用户 + dnf：不用 sudo；仓库地址换成镜像
new_case "install toolkit dnf as root with mirror"
FAKE_UID=0
EXTRA_PATH="$WORK/net:$WORK/pm-dnf"
EXTRA_ENV=(NVIDIA_TOOLKIT_REPO=https://mirror.example.com/libnvidia-container/)
run_setup --install-toolkit --yes
expect_rc 0
expect_no_log "sudo"
expect_out_order \
	"+ curl -fsSL https://mirror.example.com/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo | sed -e 's#https://nvidia.github.io/libnvidia-container#https://mirror.example.com/libnvidia-container#g' | tee /etc/yum.repos.d/nvidia-container-toolkit.repo" \
	"+ dnf install -y nvidia-container-toolkit" \
	"+ nvidia-ctk runtime configure --runtime=docker" \
	"+ systemctl restart docker"
expect_log "tee /etc/yum.repos.d/nvidia-container-toolkit.repo"
expect_log_order "dnf install -y nvidia-container-toolkit" "nvidia-ctk runtime configure --runtime=docker" "systemctl restart docker"
expect_same "仓库文件中的地址换成镜像" \
	'[nvidia-container-toolkit]
baseurl=https://mirror.example.com/libnvidia-container/stable/rpm/$basearch
gpgkey=https://mirror.example.com/libnvidia-container/gpgkey' \
	"$(cat "$FAKE_STATE/tee-nvidia-container-toolkit.repo")"
expect_env COMPOSE_PROFILES gpu

# apt + 镜像：软件源文件中的地址也换成镜像
new_case "install toolkit apt mirror"
EXTRA_ENV=(NVIDIA_TOOLKIT_REPO=https://mirror.example.com/libnvidia-container)
run_setup --install-toolkit --yes
expect_rc 0
expect_log "curl -fsSL https://mirror.example.com/libnvidia-container/gpgkey"
expect_same "软件源文件使用镜像" \
	'deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://mirror.example.com/libnvidia-container/stable/deb/$(ARCH) /
#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://mirror.example.com/libnvidia-container/experimental/deb/$(ARCH) /' \
	"$(cat "$FAKE_STATE/tee-nvidia-container-toolkit.list")"

# yum 与 zypper
new_case "install toolkit yum"
EXTRA_PATH="$WORK/net:$WORK/pm-yum"
run_setup --install-toolkit --yes
expect_rc 0
expect_out_order "| sudo tee /etc/yum.repos.d/nvidia-container-toolkit.repo" "+ sudo yum install -y nvidia-container-toolkit" "+ sudo nvidia-ctk runtime configure --runtime=docker"
expect_log_order "sudo yum install -y nvidia-container-toolkit" "sudo nvidia-ctk runtime configure --runtime=docker"

new_case "install toolkit zypper"
EXTRA_PATH="$WORK/net:$WORK/pm-zypper"
run_setup --install-toolkit --yes
expect_rc 0
expect_log_order \
	"zypper lr nvidia-container-toolkit" \
	"sudo zypper --non-interactive ar https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo" \
	"sudo zypper --non-interactive --gpg-auto-import-keys install -y nvidia-container-toolkit" \
	"sudo nvidia-ctk runtime configure --runtime=docker"
expect_env COMPOSE_PROFILES gpu

# 安装失败：退出码 1，.env 不变
new_case "install toolkit fails"
printf 'TM_DB_PASSWORD=secret\nCOMPOSE_PROFILES=\n' >"$ENV"
before="$(cat "$ENV")"
FAKE_PM_FAIL=1
run_setup --install-toolkit --yes
expect_rc 1
expect_out "命令执行失败：sudo apt-get install -y nvidia-container-toolkit"
expect_out "没有修改 .env"
expect_no_log "nvidia-ctk"
expect_same "安装失败时 .env 不变" "$before" "$(cat "$ENV")"

# 没有 sudo 的非 root 用户无法安装
new_case "install toolkit without sudo"
mkdir -p "$CASE/nosudo"
for f in "$BIN"/*; do [ "$(basename "$f")" = sudo ] || ln -s "$f" "$CASE/nosudo/"; done
FAKEBIN="$CASE/nosudo"
run_setup --install-toolkit --yes
expect_rc 1
expect_out "需要 root 权限"

# 不认识的包管理器
new_case "install toolkit unknown distro"
EXTRA_PATH="$WORK/net"
run_setup --install-toolkit --yes
expect_rc 1
expect_out "无法识别包管理器"

# --install-toolkit --dry-run：只列出命令
new_case "install toolkit dry run"
run_setup --install-toolkit --dry-run
expect_rc 0
expect_out "sudo apt-get install -y nvidia-container-toolkit"
expect_out "（--dry-run：不执行）"
expect_no_log "apt-get"
expect_no_log "nvidia-ctk"
checks=$((checks + 1))
[ ! -e "$ENV" ] || fail "--dry-run 不应创建 .env"

# 没有 GPU：CPU 模式，不运行 Docker GPU 验证
new_case "no gpu"
SMI="nvidia-smi-not-installed"
pci_device 0000:00:02.0 0x8086 0x030000
run_setup
expect_rc 0
expect_out "未检测到 NVIDIA 显卡"
expect_env COMPOSE_PROFILES cpu
expect_env TM_TORCH_VARIANT cpu
expect_no_log "docker run"
expect_no_log "docker pull"

# PCI 有 NVIDIA 显卡但没有驱动
new_case "pci gpu without driver"
SMI="nvidia-smi-not-installed"
pci_device 0000:01:00.0 0x10de 0x030000
pci_device 0000:01:00.1 0x10de 0x040300
pci_device 0000:02:00.0 0x10de 0x030200
run_setup
expect_rc 0
expect_out "检测到 2 块 NVIDIA 显卡（PCI 设备），但没有安装 NVIDIA 驱动"
expect_out "ubuntu-drivers install"
expect_out "改用 GPU：安装 NVIDIA 驱动"
expect_env COMPOSE_PROFILES cpu
expect_no_log "docker run"

# 驱动过旧：没有可用的 CUDA 版 PyTorch
new_case "old driver"
FAKE_GPUS="Tesla T4|418.226.00|7.5|15109"
run_setup
expect_rc 0
expect_out "驱动 418.226.00 过旧"
expect_out "改用 GPU：升级 NVIDIA 驱动"
expect_env COMPOSE_PROFILES cpu
expect_env TM_TORCH_VARIANT cpu
expect_no_log "docker run"

# 驱动 470：CUDA 11.8
new_case "driver 470"
with_toolkit
FAKE_GPUS="Tesla T4|470.256.02|7.5|15109"
run_setup
expect_rc 0
expect_env TM_TORCH_VARIANT cu118
expect_env COMPOSE_PROFILES gpu

# 旧版 nvidia-smi 不支持 compute_cap：按驱动选择变体
new_case "old nvidia-smi without compute_cap"
with_toolkit
FAKE_SMI_MODE=old
FAKE_GPUS="Tesla P40|470.256.02|6.1|24451"
run_setup
expect_rc 0
expect_out "计算能力 未知"
expect_out "按驱动版本选择 PyTorch 变体"
expect_env TM_TORCH_VARIANT cu118
expect_env COMPOSE_PROFILES gpu

# NVML 版本不匹配（驱动升级后没有重启）
new_case "nvml mismatch"
FAKE_SMI_MODE=mismatch
run_setup
expect_rc 0
expect_out "Driver/library version mismatch"
expect_out "重启机器"
expect_env COMPOSE_PROFILES cpu
expect_no_log "docker run"

# 驱动没有加载
new_case "driver not loaded"
FAKE_SMI_MODE=error
run_setup
expect_rc 0
expect_out "nvidia-smi 运行失败"
expect_env COMPOSE_PROFILES cpu

new_case "no devices"
FAKE_SMI_MODE=nodevices
run_setup
expect_rc 0
expect_out "nvidia-smi 没有找到显卡"
expect_env COMPOSE_PROFILES cpu

# RTX 5090（Blackwell，12.0）+ 驱动 575：需要 CUDA 12.8
new_case "rtx 5090 driver 575"
with_toolkit
FAKE_GPUS="NVIDIA GeForce RTX 5090|575.57.08|12.0|32607"
run_setup
expect_env TM_TORCH_VARIANT cu128
expect_env COMPOSE_PROFILES gpu

# 没有同时支持全部显卡的变体：选支持最多的，并提示无法使用的显卡
new_case "partially supported gpus"
with_toolkit
FAKE_GPUS="Tesla P40|580.65.06|6.1|24451
NVIDIA GeForce RTX 5090|580.65.06|12.0|32607
NVIDIA GeForce RTX 5090|580.65.06|12.0|32607"
FAKE_CONTAINER_GPUS=3
run_setup
expect_env TM_TORCH_VARIANT cu130
expect_out "计算能力为 6.1 的显卡不被 cu130 支持"

# .env 合并：保留注释、空行、顺序、其它键与文件权限；替换已有的键（含 CRLF 行），
# 注释掉的 # KEY= 不动，没有的键追加到标题下；COMPOSE_PROFILES 保留用户的其它 profile
new_case "env merge"
with_toolkit
cat >"$ENV" <<'EOF'
# 我的部署配置
TM_HTTP_PORT=9090

# ---- 数据库 ----
TM_DB_PASSWORD="p@ss#word"   # 不要泄露
COMPOSE_PROFILES=monitoring, cpu ,debug
# TM_SERVER_URL=http://example.invalid:8080
# TM_NODE_NAME=commented
EOF
printf 'TM_TORCH_VARIANT=cpu\r\nTZ=UTC' >>"$ENV"
chmod 600 "$ENV"
run_setup --server-url http://10.0.0.5:8080/ --token abcdefghijklmnop.1234
expect_rc 0
expect_same "合并结果" "# 我的部署配置
TM_HTTP_PORT=9090

# ---- 数据库 ----
TM_DB_PASSWORD=\"p@ss#word\"   # 不要泄露
COMPOSE_PROFILES=monitoring,debug,gpu
# TM_SERVER_URL=http://example.invalid:8080
# TM_NODE_NAME=commented
TM_TORCH_VARIANT=cu126$(printf '\r')
TZ=UTC

# ---- 计算节点（由 make setup / deploy/scripts/setup-compute.sh 写入，见 docs/deployment.md）----
TM_NODE_NAME=gpu-host-01
TM_HOST_HOSTNAME=gpu-host-01
TM_SERVER_URL=http://10.0.0.5:8080
TM_NODE_TOKEN=abcdefghijklmnop.1234" "$(cat "$ENV")"
expect_same "权限保持 600" 600 "$(stat -c %a "$ENV" 2>/dev/null || stat -f %Lp "$ENV")"
expect_out "TM_TORCH_VARIANT=cu126（原为 cpu）"
expect_out "COMPOSE_PROFILES=monitoring,debug,gpu（原为 monitoring, cpu ,debug）"
expect_out "TM_SERVER_URL=http://10.0.0.5:8080（新增）"
expect_no_out "abcdefghijklmnop.1234"
expect_no_out "使用了本机的主机名"
expect_out "docker compose up -d --build trainer"
# 幂等：再运行一次不改变任何内容（包括没有再追加标题）
before="$(cat "$ENV")"
run_setup --server-url http://10.0.0.5:8080 --token abcdefghijklmnop.1234
expect_rc 0
expect_out ".env 无需修改"
expect_same "第二次运行后不变" "$before" "$(cat "$ENV")"
checks=$((checks + 1))
[ -z "$(find "$CASE" -maxdepth 1 -name '.env.setup.*')" ] || fail "留下了临时文件"

# 关闭训练节点：去掉 gpu / cpu，保留其它 profile，不改变量变体
new_case "mode off"
printf 'COMPOSE_PROFILES=gpu,monitoring\nTM_TORCH_VARIANT=cu126\n' >"$ENV"
run_setup --mode off
expect_rc 0
expect_env COMPOSE_PROFILES monitoring
expect_env TM_TORCH_VARIANT cu126
expect_env TM_TRAINER_MODE off
expect_out "训练节点：不运行"
expect_no_log "docker run"
# 之后不带参数运行：沿用 .env 中的 off
run_setup
expect_out "模式：off（.env 中的 TM_TRAINER_MODE）"
expect_env COMPOSE_PROFILES monitoring

# .env 中的模式 < 环境变量 < 命令行
new_case "mode precedence"
printf 'TM_TRAINER_MODE=cpu\n' >"$ENV"
with_toolkit
run_setup
expect_env COMPOSE_PROFILES cpu
expect_no_log "docker run"
EXTRA_ENV=(TM_TRAINER_MODE=auto)
run_setup
expect_env COMPOSE_PROFILES gpu
expect_env TM_TRAINER_MODE cpu
EXTRA_ENV=(TM_TRAINER_MODE=auto)
run_setup --mode cpu
expect_env COMPOSE_PROFILES cpu
expect_env TM_TRAINER_MODE cpu

# .env 中的模式无效：警告并按 auto 处理
new_case "invalid mode in env"
printf 'TM_TRAINER_MODE=fast\n' >"$ENV"
with_toolkit
run_setup
expect_rc 0
expect_out "TM_TRAINER_MODE=fast 无效"
expect_env COMPOSE_PROFILES gpu

# 强制 GPU：没有显卡时警告，沿用 .env 中的 CUDA 变体
new_case "forced gpu without gpu"
SMI="nvidia-smi-not-installed"
printf 'TM_TORCH_VARIANT=cu128\n' >"$ENV"
run_setup --mode gpu
expect_rc 0
expect_out "仍按要求使用 GPU 模式"
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu128
# .env 中没有 CUDA 变体时用表中的第一个
new_case "forced gpu without gpu default variant"
SMI="nvidia-smi-not-installed"
run_setup --mode gpu
expect_env TM_TORCH_VARIANT cu130

# 强制 GPU：Docker 无法使用显卡时只警告
new_case "forced gpu without toolkit"
run_setup --mode gpu
expect_rc 0
expect_out "Docker 无法使用显卡（见上文），仍按要求使用 GPU 模式"
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126

# --variant 固定变体：写入 TM_TRAINER_VARIANT，之后的运行（make up）沿用，--variant auto 恢复自动选择
new_case "variant override"
with_toolkit
run_setup --variant cu118
expect_rc 0
expect_env TM_TORCH_VARIANT cu118
expect_env TM_TRAINER_VARIANT cu118
expect_out "使用 PyTorch 变体 cu118（命令行参数 --variant 指定；按驱动与显卡推荐 cu126）"
expect_out "PyTorch 变体：固定为 cu118"
EXTRA_ENV=(TM_SETUP_CALLER=up)
run_setup
expect_rc 0
expect_env TM_TORCH_VARIANT cu118
expect_out "使用 PyTorch 变体 cu118（.env 中的 TM_TRAINER_VARIANT 指定；按驱动与显卡推荐 cu126）"
expect_out ".env 无需修改"
EXTRA_ENV=()
run_setup --variant auto
expect_rc 0
expect_env TM_TRAINER_VARIANT auto
expect_env TM_TORCH_VARIANT cu126
expect_no_out "固定为"
run_setup --variant cu130
expect_out "cu130 需要驱动 ≥ 580"
expect_env TM_TORCH_VARIANT cu130

# 固定的变体只在 GPU 模式下使用；退回 CPU 时保留它，下次能用 GPU 时继续生效
new_case "variant pinned cpu fallback"
printf 'TM_TRAINER_VARIANT=cu118\n' >"$ENV"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES cpu
expect_env TM_TORCH_VARIANT cpu
expect_env TM_TRAINER_VARIANT cu118
expect_no_out "固定为"
# --mode cpu 与 --variant auto 可以一起使用（只是取消固定）
run_setup --mode cpu --variant auto
expect_rc 0
expect_env TM_TRAINER_VARIANT auto
expect_env TM_TORCH_VARIANT cpu

# 环境变量优先于 .env；无效的值警告并自动选择
new_case "variant pinned precedence"
with_toolkit
printf 'TM_TRAINER_VARIANT=cu118\n' >"$ENV"
EXTRA_ENV=(TM_TRAINER_VARIANT=cu128)
run_setup
expect_rc 0
expect_env TM_TORCH_VARIANT cu128
expect_out "环境变量 TM_TRAINER_VARIANT 指定"
EXTRA_ENV=()
printf 'TM_TRAINER_VARIANT=cu999\n' >"$ENV"
run_setup
expect_rc 0
expect_out "TM_TRAINER_VARIANT=cu999 无效"
expect_env TM_TORCH_VARIANT cu126
expect_env TM_TRAINER_VARIANT cu999
printf 'TM_TRAINER_VARIANT=cpu\n' >"$ENV"
run_setup
expect_out "CPU 训练节点请设置 TM_TRAINER_MODE=cpu"
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126

# 令牌只生成一次；已有的令牌不变
new_case "token generated once"
with_toolkit
run_setup
first="$(env_value TM_NODE_TOKEN)"
run_setup --mode cpu
expect_env TM_NODE_TOKEN "$first"
printf 'TM_NODE_TOKEN=existing-token-0123456789\n' >"$ENV"
run_setup
expect_env TM_NODE_TOKEN existing-token-0123456789
expect_no_out "existing-token-0123456789"

# 每次生成的令牌不同
new_case "token random"
run_setup --mode cpu
t1="$(env_value TM_NODE_TOKEN)"
rm -f "$ENV"
run_setup --mode cpu
t2="$(env_value TM_NODE_TOKEN)"
checks=$((checks + 1))
[ "$t1" != "$t2" ] || fail "两次生成的令牌相同：$t1"

# 远程节点（TM_SERVER_URL 指向其它机器）不生成令牌，提示使用平台的令牌
new_case "remote node without token"
with_toolkit
printf 'TM_SERVER_URL=http://192.168.1.10:8080\n' >"$ENV"
run_setup
expect_rc 0
expect_no_env TM_NODE_TOKEN
expect_out "没有设置 TM_NODE_TOKEN"
expect_out "docker compose up -d --build trainer"

# 已有 .env、--dry-run：不写文件，打印将写入的内容（令牌打码）
new_case "dry run"
with_toolkit
printf '# keep\nTM_HTTP_PORT=8080\n' >"$ENV"
before="$(cat "$ENV")"
run_setup --dry-run
expect_rc 0
expect_out "将写入 $ENV（--dry-run，未修改）"
expect_out "COMPOSE_PROFILES=gpu（新增）"
expect_out "TM_NODE_TOKEN="
expect_out "个字符）（新增）"
expect_same "--dry-run 不修改 .env" "$before" "$(cat "$ENV")"

# .env 不存在、--dry-run：不创建
new_case "dry run without env"
run_setup --dry-run
expect_rc 0
expect_out "实际运行时会从 .env.example 复制"
checks=$((checks + 1))
[ ! -e "$ENV" ] || fail "--dry-run 不应创建 .env"

# 镜像不在本地、拉取失败（离线）：无法验证，不等于不支持
new_case "offline with nvidia runtime"
FAKE_DOCKER_IMAGE=offline
FAKE_DOCKER_RUNTIMES="io.containerd.runc.v2 nvidia runc"
run_setup
expect_rc 0
expect_out "Docker GPU 支持：无法验证"
expect_out "无法拉取镜像 python:3.12-slim-bookworm"
expect_out "按支持处理"
expect_env COMPOSE_PROFILES gpu
expect_no_log "docker run"

new_case "offline without nvidia config"
FAKE_DOCKER_IMAGE=offline
run_setup
expect_rc 0
expect_out "Docker GPU 支持：无法验证"
expect_out "Docker 中没有找到 NVIDIA 的配置"
expect_out "make setup SETUP_ARGS=--install-toolkit"
expect_env COMPOSE_PROFILES cpu

# CDI 配置与 Toolkit 的 hook 也算 Docker 已配置 GPU（离线时）
new_case "offline with cdi"
FAKE_DOCKER_IMAGE=offline
echo "cdiVersion: 0.5.0" >"$CASE/cdi/nvidia.yaml"
run_setup
expect_out "CDI 配置 $CASE/cdi/nvidia.yaml"
expect_env COMPOSE_PROFILES gpu

new_case "offline with hook"
FAKE_DOCKER_IMAGE=offline
EXTRA_PATH="$WORK/net:$WORK/pm-apt:$WORK/hook"
run_setup
expect_env COMPOSE_PROFILES gpu

# --no-docker-test：只看 Docker 的配置
new_case "no docker test"
FAKE_DOCKER_RUNTIMES="io.containerd.runc.v2 nvidia runc"
run_setup --no-docker-test
expect_rc 0
expect_out "--no-docker-test，未实际验证"
expect_env COMPOSE_PROFILES gpu
expect_no_log "docker run"

new_case "no docker test without config"
run_setup --no-docker-test
expect_env COMPOSE_PROFILES cpu
expect_out "make setup SETUP_ARGS=--install-toolkit"

# 容器中的 NVML 版本不匹配：验证失败，不建议安装 Toolkit
new_case "docker run mismatch"
FAKE_DOCKER_GPU=mismatch
run_setup
expect_rc 0
expect_out "Docker GPU 支持：验证失败"
expect_out "重启机器"
expect_no_out "make setup SETUP_ARGS=--install-toolkit"
expect_env COMPOSE_PROFILES cpu

# docker run 卡住：超时后删除容器
new_case "docker run timeout"
FAKE_DOCKER_GPU=hang
EXTRA_ENV=(TM_DOCKER_TEST_TIMEOUT=1)
run_setup
expect_rc 0
expect_out "超过 1 秒没有结束"
expect_log "docker rm -f trainmaster-gpu-check-"
expect_env COMPOSE_PROFILES cpu

# 验证超时、.env 中原来是 GPU：结果未知，沿用 GPU 配置，不删除 GPU 训练节点
new_case "docker run timeout keeps gpu"
FAKE_DOCKER_GPU=hang
EXTRA_ENV=(TM_DOCKER_TEST_TIMEOUT=1)
printf 'COMPOSE_PROFILES=gpu\nTM_TORCH_VARIANT=cu126\n' >"$ENV"
touch "$FAKE_STATE/container-trainer"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126
expect_out "沿用 .env 中原来的 GPU 设置"
expect_no_out "改用 GPU"
expect_no_log "docker compose"
checks=$((checks + 1))
[ -e "$FAKE_STATE/container-trainer" ] || fail "不应删除 GPU 训练节点的容器"
# 原来是 CPU 时照常（仍为 CPU）
printf 'COMPOSE_PROFILES=cpu\nTM_TORCH_VARIANT=cpu\n' >"$ENV"
run_setup
expect_env COMPOSE_PROFILES cpu
expect_out "在 Docker 中验证显卡失败"

# 没有 timeout 命令时同样能超时
new_case "docker run timeout without timeout command"
SYS="$SYSBIN_NO_TIMEOUT"
FAKE_DOCKER_GPU=hang
EXTRA_ENV=(TM_DOCKER_TEST_TIMEOUT=1)
run_setup
expect_rc 0
expect_out "超过 1 秒没有结束"
expect_env COMPOSE_PROFILES cpu

# Docker Desktop（WSL 2）：不安装 Toolkit，提示更新驱动与 Docker Desktop
new_case "docker desktop"
FAKE_DOCKER_OS="Docker Desktop"
run_setup --install-toolkit --yes
expect_rc 0
expect_out "Docker Desktop 无法使用显卡"
expect_no_log "apt-get"
expect_env COMPOSE_PROFILES cpu

new_case "docker desktop ok"
FAKE_DOCKER_OS="Docker Desktop"
FAKE_DOCKER_GPU=ok
run_setup
expect_env COMPOSE_PROFILES gpu

# 无法连接 Docker（如当前用户不在 docker 组）：无从判断，.env 中还没有训练节点配置时什么也不写，退出码 1
new_case "docker permission denied"
FAKE_DOCKER_INFO=denied
run_setup
expect_rc 1
expect_out "无法连接 Docker 守护进程"
expect_out "usermod -aG docker"
expect_out "无法连接 Docker，无法判断训练节点能否使用显卡，没有写入 .env"
expect_no_out "训练节点：CPU"
checks=$((checks + 1))
[ ! -e "$ENV" ] || fail "无法连接 Docker 时不应创建 .env"
# 已有 .env、但还没有训练节点配置：同样不修改
printf 'TM_DB_PASSWORD=secret\nCOMPOSE_PROFILES=\n' >"$ENV"
before="$(cat "$ENV")"
run_setup --server-url http://10.0.0.5:8080 --token abcdefghijklmnop.1234
expect_rc 1
expect_same "无法连接 Docker 时 .env 不变" "$before" "$(cat "$ENV")"
# 强制模式不需要 Docker 的检查结果
run_setup --mode cpu
expect_rc 0
expect_env COMPOSE_PROFILES cpu

# 无法连接 Docker、.env 中原来是 GPU：沿用，不改成 CPU，也不删除容器
new_case "docker unreachable keeps gpu"
FAKE_DOCKER_INFO=denied
printf 'COMPOSE_PROFILES=gpu\nTM_TORCH_VARIANT=cu126\n' >"$ENV"
touch "$FAKE_STATE/container-trainer"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126
expect_out "训练节点：GPU（trainer 服务，PyTorch 变体 cu126）"
expect_out "沿用 .env 中原来的设置"
expect_no_out "改用 GPU"
expect_no_log "docker compose"
# 驱动升级后推荐的变体变了：变体不依赖 Docker，照常更新
FAKE_GPUS="NVIDIA GeForce RTX 4090|580.65.06|8.9|24564"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu130

# dockerd 没有运行、.env 中原来是 CPU：沿用 CPU
new_case "docker unreachable keeps cpu"
FAKE_DOCKER_INFO=down
printf 'COMPOSE_PROFILES=monitoring,cpu\nTM_TORCH_VARIANT=cpu\n' >"$ENV"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES monitoring,cpu
expect_env TM_TORCH_VARIANT cpu
expect_out "沿用 .env 中原来的设置"
expect_out "改用 GPU：解决 Docker 的连接问题后，重新运行 make setup"

# 没有显卡时与 Docker 无关：照常选择 CPU
new_case "docker unreachable without gpu"
FAKE_DOCKER_INFO=denied
SMI="nvidia-smi-not-installed"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES cpu
expect_out "未检测到 NVIDIA 显卡"

# 驱动更新后还没有重启（版本不匹配）、.env 中原来是 GPU：沿用 GPU 配置，不删除仍在运行的 GPU 训练节点
new_case "nvml mismatch keeps gpu"
FAKE_SMI_MODE=mismatch
printf 'COMPOSE_PROFILES=gpu\nTM_TORCH_VARIANT=cu126\n' >"$ENV"
touch "$FAKE_STATE/container-trainer"
run_setup
expect_rc 0
expect_env COMPOSE_PROFILES gpu
expect_env TM_TORCH_VARIANT cu126
expect_out "沿用 .env 中原来的 GPU 设置"
expect_out "重启机器"
expect_no_out "按驱动与显卡无法选择 CUDA 版的 PyTorch"
expect_no_log "docker compose"
checks=$((checks + 1))
[ -e "$FAKE_STATE/container-trainer" ] || fail "不应删除 GPU 训练节点的容器"

new_case "docker missing"
EXTRA_ENV=(TM_DOCKER=docker-not-installed)
run_setup
expect_rc 0
expect_out "Docker：未安装"
expect_env COMPOSE_PROFILES cpu

# 切换模式时删除另一个模式留下的训练节点容器（两者共用节点标识）
new_case "switch mode removes stale trainer"
with_toolkit
touch "$FAKE_STATE/container-trainer-cpu"
printf 'COMPOSE_PROFILES=cpu\n' >"$ENV"
run_setup
expect_env COMPOSE_PROFILES gpu
expect_log "docker compose --env-file $ENV ps -aq trainer-cpu"
expect_log "docker compose --env-file $ENV rm -s -f trainer-cpu"
expect_out "删除之前的训练节点容器（trainer-cpu 服务）"
checks=$((checks + 1))
[ ! -e "$FAKE_STATE/container-trainer-cpu" ] || fail "应删除 trainer-cpu 的容器"
# 模式不变时不检查容器
: >"$FAKE_LOG"
run_setup
expect_no_log "docker compose"

# --dry-run 不删除容器
new_case "dry run keeps containers"
with_toolkit
touch "$FAKE_STATE/container-trainer-cpu"
printf 'COMPOSE_PROFILES=cpu\n' >"$ENV"
run_setup --dry-run
expect_no_log "docker compose"

# 环境变量会覆盖 .env 时提醒
new_case "environment overrides"
with_toolkit
EXTRA_ENV=(COMPOSE_PROFILES=cpu TM_NODE_TOKEN=shell-token-0123456789)
printf 'TM_NODE_TOKEN=file-token-0123456789ab\n' >"$ENV"
run_setup
expect_out "环境变量 COMPOSE_PROFILES 已设置为“cpu”"
expect_out "环境变量 TM_NODE_TOKEN 已设置为“shel…（22 个字符）”"
expect_no_out "shell-token-0123456789"

# make up 调用时提示接下来的步骤：先启动平台，再启动训练节点
new_case "called from make up"
with_toolkit
EXTRA_ENV=(TM_SETUP_CALLER=up)
run_setup
expect_out "接下来先构建并启动平台（postgres 与 app），再构建并启动训练节点（trainer）"
expect_no_out "下一步："
run_setup --mode off
expect_out "接下来构建并启动平台（postgres 与 app）…"

# TM_HOST_HOSTNAME（app 与训练节点容器的主机名）每次写入本机当前的主机名；TM_NODE_NAME 已有时不变
new_case "host hostname"
with_toolkit
printf 'TM_NODE_NAME=my-node\nTM_HOST_HOSTNAME=old-name\n' >"$ENV"
FAKE_HOSTNAME=renamed-host
run_setup
expect_rc 0
expect_env TM_HOST_HOSTNAME renamed-host
expect_env TM_NODE_NAME my-node
expect_out "TM_HOST_HOSTNAME=renamed-host（原为 old-name）"
# 关闭训练节点时也写入（app 使用它）
run_setup --mode off
FAKE_HOSTNAME=third-name
run_setup
expect_env TM_HOST_HOSTNAME third-name
expect_env TM_NODE_NAME my-node

# 把 .env 迁移到新服务器（docs/deployment.md 第 6 节）：TM_NODE_NAME 仍是 make setup 写入的旧主机名
# （等于原来的 TM_HOST_HOSTNAME）时跟随新的主机名，新服务器的训练节点不会以旧服务器的名字注册
new_case "node name follows hostname"
with_toolkit
FAKE_HOSTNAME=old-server
run_setup
expect_env TM_NODE_NAME old-server
expect_env TM_HOST_HOSTNAME old-server
FAKE_HOSTNAME=new-server
EXTRA_ENV=(TM_SETUP_CALLER=up)
run_setup
expect_rc 0
expect_env TM_NODE_NAME new-server
expect_env TM_HOST_HOSTNAME new-server
expect_out "TM_NODE_NAME=new-server（原为 old-server）"
# 主机名没有变化时不改动
run_setup
expect_env TM_NODE_NAME new-server
expect_out ".env 无需修改"
# 关闭训练节点时同样跟随（以后改回 auto 时名字正确）
FAKE_HOSTNAME=third-server
run_setup --mode off
expect_env TM_NODE_NAME third-server
expect_env TM_HOST_HOSTNAME third-server

# 没有 TM_HOST_HOSTNAME 的旧 .env，或用户自己设置的名称：TM_NODE_NAME 不变
new_case "node name without previous hostname"
with_toolkit
printf 'TM_NODE_NAME=old-server\n' >"$ENV"
FAKE_HOSTNAME=new-server
run_setup
expect_rc 0
expect_env TM_NODE_NAME old-server
expect_env TM_HOST_HOSTNAME new-server
# 关闭训练节点时，空的 TM_NODE_NAME 保持为空，不写入主机名（与之前相同）
new_case "node name empty with mode off"
with_toolkit
printf 'TM_NODE_NAME=\nTM_HOST_HOSTNAME=old-server\n' >"$ENV"
FAKE_HOSTNAME=new-server
run_setup --mode off
expect_rc 0
expect_env TM_NODE_NAME ""
expect_env TM_HOST_HOSTNAME new-server

# TM_SERVER_URL 使用本机的主机名：在训练节点容器中它指向容器自己，提醒改用 http://app:8080 或 IP
new_case "server url is own hostname"
with_toolkit
run_setup --server-url http://GPU-Host-01:8080 --token abcdefghijklmnop.1234
expect_rc 0
expect_out "使用了本机的主机名"
run_setup --server-url http://admin@gpu-host-01/trainmaster --token abcdefghijklmnop.1234
expect_out "使用了本机的主机名"
run_setup --server-url http://gpu-host-02:8080 --token abcdefghijklmnop.1234
expect_no_out "使用了本机的主机名"
# 主机名带域名时，Docker 在容器的 /etc/hosts 中同时写入第一个点之前的部分
FAKE_HOSTNAME=gpu-host-01.lab.example
run_setup --server-url http://gpu-host-01:8080 --token abcdefghijklmnop.1234
expect_out "使用了本机的主机名"
run_setup --server-url http://gpu-host-01.lab.example:8080 --token abcdefghijklmnop.1234
expect_out "使用了本机的主机名"
run_setup --server-url http://gpu-host-01.other.example:8080 --token abcdefghijklmnop.1234
expect_no_out "使用了本机的主机名"

# 本机的主机名与 compose 中的服务名（app、postgres）相同：容器中这个名字会指向容器自己，
# app 连不上 postgres、本机的训练节点连不上 app。TM_HOST_HOSTNAME 留空，使用 compose 中的默认值
new_case "host hostname clashes with service"
with_toolkit
printf 'TM_HOST_HOSTNAME=old-name\n' >"$ENV"
FAKE_HOSTNAME=Postgres.lab.example
run_setup
expect_rc 0
expect_out "与 docker-compose.yml 中的服务名相同"
expect_env TM_HOST_HOSTNAME ""
expect_env TM_NODE_NAME Postgres.lab.example
FAKE_HOSTNAME=app
run_setup
expect_out "与 docker-compose.yml 中的服务名相同"
expect_env TM_HOST_HOSTNAME ""
# 只是包含服务名时照常写入
FAKE_HOSTNAME=app-server
run_setup
expect_no_out "与 docker-compose.yml 中的服务名相同"
expect_env TM_HOST_HOSTNAME app-server
# 没有 TM_HOST_HOSTNAME 的 .env 不追加空值
new_case "host hostname clashes without key"
with_toolkit
printf 'TM_DB_PASSWORD=secret\n' >"$ENV"
FAKE_HOSTNAME=app
run_setup
expect_rc 0
checks=$((checks + 1))
grep -q '^TM_HOST_HOSTNAME=' "$ENV" && fail "不应写入空的 TM_HOST_HOSTNAME"

# snap 版 Docker：不读取 /etc/docker/daemon.json，也没有 docker.service，只打印手动步骤
new_case "install toolkit snap docker"
FAKE_DOCKER_ROOT=/var/snap/docker/common/var-lib-docker
run_setup --install-toolkit --yes
expect_rc 0
expect_out "这台机器的 Docker 是 snap 版"
expect_out "sudo apt-get install -y nvidia-container-toolkit"
expect_out "sudo snap restart docker"
expect_out "改用 GPU：按上面的步骤手动安装并配置 NVIDIA Container Toolkit 后，重新运行 make setup"
expect_no_out "sudo nvidia-ctk runtime configure --runtime=docker"
expect_no_out "sudo systemctl restart docker"
expect_no_log "apt-get"
expect_no_log "nvidia-ctk"
expect_no_log "systemctl"
expect_no_log "sudo"
expect_env COMPOSE_PROFILES cpu
# 不加 --install-toolkit 时同样只给出手动步骤，不建议自动安装
run_setup
expect_out "这台机器的 Docker 是 snap 版"
expect_no_out "自动安装（需要 root 或 sudo"

# rootless Docker：配置写到当前用户的 daemon.json，用 systemctl --user 重启
new_case "install toolkit rootless docker"
FAKE_DOCKER_SECURITY="[name=seccomp,profile=builtin name=rootless name=cgroupns]"
run_setup --install-toolkit --yes
expect_rc 0
expect_out "以 rootless 模式运行"
expect_out "sudo apt-get install -y nvidia-container-toolkit"
expect_out 'nvidia-ctk runtime configure --runtime=docker --config=$HOME/.config/docker/daemon.json'
expect_out "systemctl --user restart docker"
expect_out "sudo nvidia-ctk config --set nvidia-container-cli.no-cgroups --in-place"
expect_no_log "apt-get"
expect_no_log "nvidia-ctk"
expect_no_log "systemctl"
expect_env COMPOSE_PROFILES cpu
# 已安装 Toolkit 时不再列出安装软件包的命令
new_case "rootless docker toolkit installed"
toolkit_installed_only
FAKE_DOCKER_SECURITY="[name=seccomp,profile=builtin name=rootless name=cgroupns]"
run_setup --install-toolkit --yes
expect_rc 0
expect_no_out "apt-get install -y nvidia-container-toolkit"
expect_out "systemctl --user restart docker"
expect_no_log "nvidia-ctk"

# 没有运行 systemd 的 WSL：不执行，给出手动步骤与启用 systemd 的方法
new_case "install toolkit wsl without systemd"
echo "5.15.153.1-microsoft-standard-WSL2" >"$CASE/osrelease"
rmdir "$CASE/run-systemd"
run_setup --install-toolkit --yes
expect_rc 0
expect_out "这台 WSL 中没有运行 systemd"
expect_out "sudo nvidia-ctk runtime configure --runtime=docker"
expect_out "sudo service docker restart"
expect_out "/etc/wsl.conf"
expect_out "wsl --shutdown"
expect_no_log "apt-get"
expect_no_log "nvidia-ctk"
expect_no_log "systemctl"
expect_no_log "service"
expect_env COMPOSE_PROFILES cpu

# 启用了 systemd 的 WSL：与普通 Linux 相同
new_case "install toolkit wsl with systemd"
echo "5.15.153.1-microsoft-standard-WSL2" >"$CASE/osrelease"
run_setup --install-toolkit --yes
expect_rc 0
expect_log_order "sudo apt-get install -y nvidia-container-toolkit" "sudo nvidia-ctk runtime configure --runtime=docker" "sudo systemctl restart docker"
expect_env COMPOSE_PROFILES gpu

# 没有运行 systemd 的 Linux（有 systemctl 命令也不用）：用 service 重启 Docker
new_case "install toolkit without systemd"
rmdir "$CASE/run-systemd"
run_setup --install-toolkit --yes
expect_rc 0
expect_log_order "sudo nvidia-ctk runtime configure --runtime=docker" "sudo service docker restart"
expect_no_log "systemctl"
expect_env COMPOSE_PROFILES gpu
# 没有安装 Toolkit 时的提示也用 service
new_case "toolkit hint without systemd"
rmdir "$CASE/run-systemd"
run_setup
expect_out "sudo service docker restart"
expect_no_out "systemctl"

# 既没有 systemd 也没有 service：安装后提醒手动重启 Docker
new_case "install toolkit without service manager"
rmdir "$CASE/run-systemd"
mkdir -p "$CASE/nosvc"
for f in "$BIN"/*; do
	case "$(basename "$f")" in systemctl | service) ;; *) ln -s "$f" "$CASE/nosvc/" ;; esac
done
FAKEBIN="$CASE/nosvc"
run_setup
expect_out "然后重启 Docker"
run_setup --install-toolkit --yes
expect_rc 0
expect_out "找不到 systemctl / service，请手动重启 Docker"

# 没有 .env.example 时新建 .env
new_case "no env example"
mv "$REPO/.env.example" "$WORK/env.example.saved"
with_toolkit
run_setup
mv "$WORK/env.example.saved" "$REPO/.env.example"
expect_rc 0
expect_env COMPOSE_PROFILES gpu
expect_same "新建的 .env 以标题开头" \
	"# ---- 计算节点（由 make setup / deploy/scripts/setup-compute.sh 写入，见 docs/deployment.md）----" \
	"$(head -n 1 "$ENV")"

# 参数错误：退出码 2，不修改任何文件
for args in "--bogus" "--mode" "--mode fast" "--mode=" "--token short" "--token has\$dollar-0123456789" \
	"--server-url ftp://x" "--server-url http://a b" "--variant cu999" "--variant cpu" "--mode cpu --variant cu126"; do
	new_case "bad args: $args"
	# shellcheck disable=SC2086 # 按空白拆分为多个参数
	run_setup $args
	expect_rc 2
	checks=$((checks + 1))
	[ ! -e "$ENV" ] || fail "参数错误时不应创建 .env"
done

# 方括号中的 IPv6 地址（页面以 http://[fd00::10]:8080 打开时）：训练节点容器的网络默认没有 IPv6，明确说明原因
for url in "http://[fd00::10]:8080" "https://[2001:db8::1]/trainmaster" "http://admin@[fd00::10]:8080"; do
	new_case "ipv6 server url: $url"
	run_setup --server-url "$url" --token abcdefghijklmnop.1234
	expect_rc 2
	expect_out "--server-url 不能使用 IPv6 地址：训练节点容器的 Docker 网络默认没有 IPv6，请改用平台的 IPv4 地址或域名"
	expect_no_out "必须是 http:// 或 https:// 开头的地址"
	checks=$((checks + 1))
	[ ! -e "$ENV" ] || fail "参数错误时不应创建 .env"
done

new_case "bad toolkit repo"
EXTRA_ENV=(NVIDIA_TOOLKIT_REPO="ftp://mirror")
run_setup
expect_rc 2
expect_out "NVIDIA_TOOLKIT_REPO"

new_case "help"
run_setup --help
expect_rc 0
expect_out "用法：deploy/scripts/setup-compute.sh"
expect_out "cu130 / cu126 / cu128 / cu118"
checks=$((checks + 1))
[ ! -e "$ENV" ] || fail "--help 不应创建 .env"

if [ "$fails" -gt 0 ]; then
	echo "setup-compute: $fails 项失败（共 $checks 项检查）" >&2
	exit 1
fi
echo "setup-compute: 全部通过（$checks 项检查）"
