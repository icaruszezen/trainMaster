#!/usr/bin/env bash
# 在不使用 Docker 的机器上安装 trainMaster 训练节点（裸机安装）：
#   检测 nvidia-smi → 按驱动与显卡选择 PyTorch 变体 → 创建虚拟环境 → 安装 PyTorch（install-torch.sh）
#   → 安装 requirements.txt 与训练节点 → 写入配置文件 → 自检 → 可选生成并启用 systemd 服务。
# 可以重复运行：已有的虚拟环境会复用，PyTorch 变体变化时自动重新安装。
#
# 用法：sudo trainer/scripts/install.sh --server-url http://192.168.1.10:8080 --token <令牌> --systemd
#
# 环境变量：
#   PIP_INDEX_URL / TORCH_INDEX_URL / TORCH_FIND_LINKS  PyPI 与 PyTorch 镜像（见 install-torch.sh）
#   TM_NVIDIA_SMI         检测显卡使用的 nvidia-smi（默认在 PATH 中查找）
#   TM_MACHINE            覆盖 uname -m 的结果（x86_64 / aarch64）
#   TM_SYSTEMD_UNIT_DIR   systemd 服务文件所在目录（默认 /etc/systemd/system）
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
trainer_dir="$(dirname "$script_dir")"
# shellcheck source=SCRIPTDIR/lib/torch-variant.sh
. "$script_dir/lib/torch-variant.sh"

SERVICE_NAME="trainmaster-trainer.service"
# 旧驱动的 nvidia-smi 不支持查询 compute_cap，此时按 7.5（Turing）估计，与训练节点（issues.ASSUMED_CC）一致。
ASSUMED_CC="7.5"

usage() {
	cat <<'EOF'
用法：install.sh --server-url URL [选项]

在本机安装 trainMaster 训练节点（不使用 Docker）：自动检测显卡与驱动，安装匹配的 CUDA 版
PyTorch 与 Ultralytics，写入配置文件，并可注册为 systemd 服务开机自启。

必填：
  --server-url URL    平台地址，例如 http://192.168.1.10:8080

选项：
  --token TOKEN       节点令牌（平台 .env 中的 TM_NODE_TOKEN；平台未设置令牌时可省略）
  --name NAME         节点名称（默认为主机名）
  --variant VARIANT   手动指定 PyTorch 变体：cu130、cu126、cu128、cu118、cpu（默认自动选择）
  --prefix DIR        安装目录，即虚拟环境（默认 /opt/trainmaster-trainer）
  --python PY         创建虚拟环境使用的 Python（需要 3.10 或更新，默认 python3）
  --state-dir DIR     状态目录：节点标识与心跳文件（默认 /var/lib/trainmaster）
  --data-dir DIR      数据目录：上报其磁盘空间（默认 /data，不存在时使用状态目录）
  --env-file FILE     配置文件路径（默认 <prefix>/trainer.env，权限 600）
  --systemd           生成并启用 systemd 服务 trainmaster-trainer（需要 root）
  --user USER         运行训练节点的用户（默认 root）；状态目录的属主会改为该用户
  --dry-run           只打印检测结果和将要执行的步骤，不做任何修改
  -h, --help          显示本帮助
EOF
}

die() { # die <退出码> <消息>
	echo "install: $2" >&2
	exit "$1"
}

info() { echo "install: $*"; }

# ---- 参数 ----

server_url=""
token=""
name=""
variant=""
prefix="/opt/trainmaster-trainer"
python=""
state_dir="/var/lib/trainmaster"
data_dir="/data"
env_file=""
systemd=0
run_user=""
dry_run=0

need_value() { # need_value <选项> <剩余参数个数> [值]
	if [ "$2" -lt 2 ] || [ -z "${3:-}" ]; then
		usage >&2
		die 2 "$1 需要一个参数"
	fi
}

while [ $# -gt 0 ]; do
	case "$1" in
	--server-url | --token | --name | --variant | --prefix | --python | --state-dir | --data-dir | --env-file | --user)
		need_value "$1" $# "${2:-}"
		opt="$1" value="$2"
		shift 2
		;;
	--*=*)
		opt="${1%%=*}" value="${1#*=}"
		case "$opt" in
		--server-url | --token | --name | --variant | --prefix | --python | --state-dir | --data-dir | --env-file | --user) ;;
		*)
			usage >&2
			die 2 "未知选项：$1"
			;;
		esac
		[ -n "$value" ] || die 2 "$opt 需要一个参数"
		shift
		;;
	--systemd)
		systemd=1
		shift
		continue
		;;
	--dry-run)
		dry_run=1
		shift
		continue
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		die 2 "未知参数：$1"
		;;
	esac
	case "$opt" in
	--server-url) server_url="$value" ;;
	--token) token="$value" ;;
	--name) name="$value" ;;
	--variant) variant="$value" ;;
	--prefix) prefix="$value" ;;
	--python) python="$value" ;;
	--state-dir) state_dir="$value" ;;
	--data-dir) data_dir="$value" ;;
	--env-file) env_file="$value" ;;
	--user) run_user="$value" ;;
	esac
done

# ---- 校验 ----

# 按字节判断（C locale）：UTF-8 中文不会被当作控制字符或可见 ASCII。
has_control_chars() {
	local LC_ALL=C
	[[ "$1" == *[[:cntrl:]]* ]]
}

is_visible_ascii() {
	local LC_ALL=C
	[[ "$1" =~ ^[!-~]+$ ]]
}

[ -n "$server_url" ] || {
	usage >&2
	die 2 "缺少 --server-url（平台地址，例如 http://192.168.1.10:8080）"
}
if has_control_chars "$server_url" || [[ ! "$server_url" =~ ^https?://[^[:space:]/?#@]+(/[^[:space:]?#]*)?$ ]]; then
	die 2 "--server-url 不合法：$server_url（应为 http://主机:端口，不能包含用户名、查询参数或空格）"
fi
while [ "${server_url%/}" != "$server_url" ]; do server_url="${server_url%/}"; done

if [ -n "$token" ] && { ! is_visible_ascii "$token" || [ "${#token}" -gt 256 ]; }; then
	die 2 "--token 只能包含可见的 ASCII 字符（不能有空格或中文），最多 256 个字符"
fi

if [ -z "$name" ]; then
	name="$(uname -n 2>/dev/null || true)"
	[ -n "$name" ] || name="trainer"
elif has_control_chars "$name"; then
	die 2 "--name 不能包含换行等控制字符"
fi

check_dir() { # check_dir <选项> <路径>：绝对路径，不含控制字符（写入配置文件时会加引号）
	case "$2" in
	/*) ;;
	*) die 2 "$1 必须是绝对路径：$2" ;;
	esac
	if has_control_chars "$2"; then
		die 2 "$1 不能包含换行等控制字符"
	fi
}

check_unit_path() { # check_unit_path <选项> <路径>：写入 systemd 服务文件的路径还不能含空白、引号与 %
	check_dir "$1" "$2"
	if [[ "$2" == *[[:space:]\"\'\\%\$\`]* ]]; then
		die 2 "$1 不能包含空白、引号、反斜杠、% 或 \$ 等字符：$2"
	fi
}

while [ "${prefix%/}" != "$prefix" ]; do prefix="${prefix%/}"; done
[ -n "$prefix" ] || die 2 "--prefix 不能是根目录"
check_unit_path --prefix "$prefix"
[ -n "$env_file" ] || env_file="$prefix/trainer.env"
check_unit_path --env-file "$env_file"
check_dir --state-dir "$state_dir"
check_dir --data-dir "$data_dir"

if [ -n "$run_user" ] && [[ ! "$run_user" =~ ^[A-Za-z_][A-Za-z0-9._-]*\$?$ ]]; then
	die 2 "--user 不是合法的用户名：$run_user"
fi

variant_names="$(tm_variant_names | tr '\n' ' ' | sed 's/ $//')"
if [ -n "$variant" ] && ! tm_variant_field "$variant" torch >/dev/null; then
	die 2 "未知的变体 $variant（可选：$variant_names）"
fi

unit_dir="${TM_SYSTEMD_UNIT_DIR:-/etc/systemd/system}"
unit_file="$unit_dir/$SERVICE_NAME"
venv_python="$prefix/bin/python"

# ---- 检测显卡 ----

gpu_lines=()
gpu_ccs=()
driver=""
cc_assumed=0
smi_error=""

trim() { # trim <文本>：去掉首尾空白
	local s="$1"
	s="${s#"${s%%[![:space:]]*}"}"
	s="${s%"${s##*[![:space:]]}"}"
	printf '%s' "$s"
}

detect_gpus() {
	local smi="${TM_NVIDIA_SMI:-nvidia-smi}" out with_cc=1 line n gpu_name drv cc mem
	local -a fields
	if ! command -v "$smi" >/dev/null 2>&1; then
		smi_error="找不到 nvidia-smi"
		return 0
	fi
	if ! out="$("$smi" --query-gpu=name,driver_version,compute_cap,memory.total --format=csv,noheader 2>&1)"; then
		# 驱动 < 510 的 nvidia-smi 不认识 compute_cap 字段
		if out="$("$smi" --query-gpu=name,driver_version,memory.total --format=csv,noheader 2>&1)"; then
			with_cc=0
		else
			smi_error="nvidia-smi 运行失败：$(printf '%s\n' "$out" | head -n 1)"
			return 0
		fi
	fi
	while IFS= read -r line; do
		IFS=',' read -r -a fields <<<"$line"
		n=${#fields[@]}
		# 名称中可能含逗号：其余字段从右往左取
		if [ "$with_cc" = 1 ]; then
			[ "$n" -ge 4 ] || continue
			cc="$(trim "${fields[n - 2]}")"
			drv="$(trim "${fields[n - 3]}")"
			gpu_name="$(IFS=,; trim "${fields[*]:0:n-3}")"
		else
			[ "$n" -ge 3 ] || continue
			cc=""
			drv="$(trim "${fields[n - 2]}")"
			gpu_name="$(IFS=,; trim "${fields[*]:0:n-2}")"
		fi
		mem="$(trim "${fields[n - 1]}")"
		if [[ ! "$cc" =~ ^[0-9]+\.[0-9]+$ ]]; then
			cc="$ASSUMED_CC"
			cc_assumed=1
		fi
		if [ -z "$driver" ] && [[ "$drv" =~ ^[0-9]+(\.[0-9]+)*$ ]]; then
			driver="$drv"
		fi
		gpu_ccs+=("$cc")
		gpu_lines+=("$gpu_name（显存 $mem，计算能力 $cc）")
	done <<<"$out"
	if [ "${#gpu_ccs[@]}" -eq 0 ]; then
		smi_error="nvidia-smi 没有列出显卡：$(printf '%s\n' "$out" | head -n 1)"
	fi
}

detect_gpus
machine="${TM_MACHINE:-$(uname -m)}"
tm_select_variant "$driver" "${gpu_ccs[*]}" "$machine"
recommended="$TM_VARIANT"
[ -n "$variant" ] || variant="$recommended"

echo "== 检测结果 =="
echo "平台：$machine"
if [ "${#gpu_ccs[@]}" -gt 0 ]; then
	echo "显卡：${#gpu_ccs[@]} 块"
	for i in "${!gpu_lines[@]}"; do
		echo "  [$i] ${gpu_lines[i]}"
	done
	echo "驱动：${driver:-未知}"
	if [ "$cc_assumed" = 1 ]; then
		echo "注意：无法查询部分显卡的计算能力（驱动较旧），按 $ASSUMED_CC 估计"
	fi
elif [ "$smi_error" = "找不到 nvidia-smi" ]; then
	echo "显卡：未检测到 NVIDIA 显卡（找不到 nvidia-smi）"
else
	echo "显卡：无法检测（$smi_error）。如果本机有 NVIDIA 显卡，请检查驱动是否安装正确"
fi
echo "推荐的 PyTorch 变体：$recommended —— $TM_VARIANT_REASON"
if [ "$variant" != "$recommended" ]; then
	echo "使用手动指定的变体：$variant"
fi
if [ "$variant" = cpu ] && [ "${#gpu_ccs[@]}" -gt 0 ]; then
	echo "注意：将安装 CPU 版 PyTorch，训练不会使用显卡"
fi
echo

# ---- 执行 ----

run() { # run <命令…>：打印并执行（--dry-run 时只打印）
	printf '+'
	printf ' %q' "$@"
	printf '\n'
	[ "$dry_run" = 1 ] || "$@"
}

# env_quote <值>：双引号形式，systemd 的 EnvironmentFile 与 POSIX shell 都按同样规则解析。
env_quote() {
	local v="$1"
	v="${v//\\/\\\\}"
	v="${v//\"/\\\"}"
	v="${v//\$/\\\$}"
	v="${v//\`/\\\`}"
	printf '"%s"' "$v"
}

env_content() { # env_content <令牌的显示形式>
	cat <<EOF
# trainMaster 训练节点配置（由 trainer/scripts/install.sh 生成，可手动修改，修改后重启服务）。
# 格式同时兼容 systemd 的 EnvironmentFile 与 shell：set -a; . $env_file; set +a
TM_SERVER_URL=$(env_quote "$server_url")
TM_NODE_TOKEN=$1
TM_NODE_NAME=$(env_quote "$name")
TM_TORCH_VARIANT=$(env_quote "$variant")
TM_STATE_DIR=$(env_quote "$state_dir")
TM_DATA_DIR=$(env_quote "$data_dir")
EOF
}

unit_content() {
	cat <<EOF
[Unit]
Description=trainMaster 训练节点
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EOF
	if [ -n "$run_user" ] && [ "$run_user" != root ]; then
		echo "User=$run_user"
	fi
	cat <<EOF
EnvironmentFile=$env_file
ExecStart=$venv_python -m trainmaster_trainer run
Restart=always
RestartSec=5
TimeoutStopSec=30
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
}

# write_file <路径> <权限>：把标准输入写入文件（先写临时文件再改名）。
write_file() {
	local path="$1" mode="$2" tmp
	tmp="$(mktemp "$(dirname "$path")/.$(basename "$path").XXXXXX")"
	if ! cat >"$tmp" || ! chmod "$mode" "$tmp" || ! mv -f "$tmp" "$path"; then
		rm -f "$tmp"
		die 1 "无法写入 $path"
	fi
}

python_ok() { # python_ok <解释器>：能运行且版本 ≥ 3.10
	"$1" -c 'import sys; raise SystemExit(sys.version_info < (3, 10))' >/dev/null 2>&1
}

if [ "$dry_run" = 0 ]; then
	# 先检查前提条件，避免装到一半才失败
	if [ -n "$run_user" ] && ! id -u "$run_user" >/dev/null 2>&1; then
		die 1 "用户 $run_user 不存在"
	fi
	if [ "$systemd" = 1 ]; then
		command -v systemctl >/dev/null 2>&1 || die 1 "找不到 systemctl：本机没有使用 systemd，请去掉 --systemd 并按结尾的说明手动运行"
		if [ ! -d "$unit_dir" ] || [ ! -w "$unit_dir" ]; then
			die 1 "无法写入 $unit_dir：--systemd 需要 root 权限（请用 sudo 运行）"
		fi
	fi
	if [ -x "$venv_python" ]; then
		python_ok "$venv_python" || die 1 "已有的虚拟环境 $prefix 无法运行或 Python 低于 3.10（可能系统 Python 已升级），请删除该目录后重新运行"
	else
		if [ -e "$prefix" ] && [ -n "$(ls -A "$prefix" 2>/dev/null)" ]; then
			die 1 "$prefix 已存在且不是虚拟环境，请换一个 --prefix 或先清空该目录"
		fi
		if [ -z "$python" ]; then
			python=python3
			command -v python3 >/dev/null 2>&1 || python=python
		fi
		command -v "$python" >/dev/null 2>&1 || die 1 "找不到 Python：$python（请安装 Python 3.10 或更新版本，或用 --python 指定）"
		python_ok "$python" || die 1 "$python 的版本低于 3.10，请安装更新的 Python 并用 --python 指定"
		"$python" -c 'import ensurepip, venv' >/dev/null 2>&1 ||
			die 1 "$python 缺少 venv 模块：Debian / Ubuntu 请先运行 sudo apt install python3-venv"
	fi
fi

echo "== 安装步骤 =="
if [ -x "$venv_python" ]; then
	info "复用已有的虚拟环境 $prefix"
else
	run "${python:-python3}" -m venv "$prefix"
fi
run "$venv_python" -m pip install --upgrade pip
run bash "$script_dir/install-torch.sh" "$variant" --python "$venv_python"
if [ "$dry_run" = 1 ]; then
	bash "$script_dir/install-torch.sh" "$variant" --python "$venv_python" --dry-run | sed 's/^/    /'
fi
run "$venv_python" -m pip install -r "$trainer_dir/requirements.txt"
run "$venv_python" -m pip install --no-deps --force-reinstall "$trainer_dir"

run mkdir -p "$state_dir"
if [ -n "$run_user" ]; then
	run chown "$run_user:" "$state_dir"
fi
if [ ! -d "$data_dir" ]; then
	info "数据目录 $data_dir 不存在：训练节点将上报状态目录所在磁盘的空间（创建该目录后重启即可）"
fi

echo
echo "== 配置文件 $env_file（权限 600）=="
if [ "$dry_run" = 1 ]; then
	if [ -n "$token" ]; then
		env_content "\"<已隐藏，共 ${#token} 个字符>\""
	else
		env_content '""'
	fi
else
	mkdir -p "$(dirname "$env_file")"
	env_content "$(env_quote "$token")" | write_file "$env_file" 600
	info "已写入 $env_file"
fi
if [ -n "$run_user" ] && [ "$systemd" = 0 ]; then
	# 手动运行时由该用户读取配置文件（systemd 以 root 读取，不需要）
	run chown "$run_user:" "$env_file"
fi

echo
echo "== 自检 =="
# 使用临时状态目录：以 root 自检时不会在正式的状态目录中留下 root 所有的文件。
if [ "$dry_run" = 1 ]; then
	echo "+ $venv_python -m trainmaster_trainer selftest"
	selftest_ok=1
else
	check_dir="$(mktemp -d)"
	if TM_STATE_DIR="$check_dir" TM_DATA_DIR="$check_dir" TM_TORCH_VARIANT="$variant" HOME="$check_dir" \
		"$venv_python" -m trainmaster_trainer selftest; then
		selftest_ok=1
	else
		selftest_ok=0
	fi
	rm -rf "$check_dir"
fi

if [ "$systemd" = 1 ]; then
	echo
	echo "== systemd 服务 $unit_file =="
	if [ "$dry_run" = 1 ]; then
		unit_content
	else
		unit_content | write_file "$unit_file" 644
		info "已写入 $unit_file"
	fi
	run systemctl daemon-reload
	run systemctl enable "$SERVICE_NAME"
	run systemctl restart "$SERVICE_NAME"
fi

echo
if [ "$dry_run" = 1 ]; then
	echo "以上为预览（--dry-run），没有做任何修改。"
	exit 0
fi
if [ "$selftest_ok" = 1 ]; then
	echo "安装完成：PyTorch 变体 $variant，自检通过。"
else
	echo "安装完成，但自检发现问题（见上面的输出）。修复后可运行：$venv_python -m trainmaster_trainer selftest"
fi
if [ "$systemd" = 1 ]; then
	echo "训练节点已作为 systemd 服务运行："
	echo "  查看状态：systemctl status trainmaster-trainer"
	echo "  查看日志：journalctl -u trainmaster-trainer -f"
	echo "  修改配置：编辑 $env_file 后运行 systemctl restart trainmaster-trainer"
else
	echo "手动运行训练节点："
	echo "  set -a; . $env_file; set +a; $venv_python -m trainmaster_trainer run"
	echo "注册为开机自启的服务：重新运行本脚本并加上 --systemd（需要 root）"
fi
echo '节点上线后会出现在平台的“计算资源”页面。'
