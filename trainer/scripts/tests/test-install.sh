#!/usr/bin/env bash
# trainer/scripts/install.sh 与 install-torch.sh 的测试：bash trainer/scripts/tests/test-install.sh
# 用假的 nvidia-smi、python、systemctl，不访问网络，不修改系统目录。
# shellcheck disable=SC2016  # 单引号中的 $ 与反引号是有意的：测试配置文件的转义
set -euo pipefail
cd "$(dirname "$0")"
scripts="$(cd .. && pwd)"
install="$scripts/install.sh"
install_torch="$scripts/install-torch.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"

fails=0
out=""
code=0

fail() {
	echo "FAIL $1" >&2
	fails=$((fails + 1))
}
run_cmd() { # run_cmd <命令…>：输出（含 stderr）存入 out，退出码存入 code
	set +e
	out="$("$@" 2>&1)"
	code=$?
	set -e
}
expect_code() { # expect_code <name> <code>
	if [ "$code" != "$2" ]; then
		fail "$1: exit code $code, expected $2"
		printf '%s\n' "$out" | sed 's/^/    | /' >&2
	fi
}
contains() { # contains <name> <text>
	if [[ "$out" != *"$2"* ]]; then
		fail "$1: output does not contain [$2]"
		printf '%s\n' "$out" | sed 's/^/    | /' >&2
	fi
}
not_contains() { # not_contains <name> <text>
	if [[ "$out" == *"$2"* ]]; then
		fail "$1: output contains [$2]"
	fi
}
check() { # check <name> <expected> <actual>
	if [ "$2" != "$3" ]; then
		fail "$1: expected [$2], got [$3]"
	fi
}

# fake_smi <名称> <compute_cap 查询的输出> [旧驱动查询的输出] [前者的退出码] [后者的退出码]
fake_smi() {
	local path="$tmp/smi-$1"
	{
		echo '#!/usr/bin/env bash'
		echo 'case "$*" in'
		printf '*compute_cap*) printf %%s %q; exit %s ;;\n' "$2" "${4:-0}"
		printf '*) printf %%s %q; exit %s ;;\n' "${3:-}" "${5:-0}"
		echo 'esac'
	} >"$path"
	chmod +x "$path"
	printf '%s' "$path"
}

dry() { # dry <nvidia-smi> <参数…>：以 --dry-run 运行 install.sh（不读取本机已安装的 systemd 服务）
	local smi="$1"
	shift
	run_cmd env -u PIP_INDEX_URL -u TORCH_INDEX_URL -u TORCH_FIND_LINKS TM_NVIDIA_SMI="$smi" \
		TM_MACHINE="${MACHINE:-x86_64}" TM_SYSTEMD_UNIT_DIR="${UNIT_DIR:-$tmp/no-units}" \
		bash "$install" --dry-run "$@"
}

URL=http://10.0.0.5:8080
smi_4090="$(fake_smi 4090 $'NVIDIA GeForce RTX 4090, 580.82.07, 8.9, 24564 MiB\nNVIDIA GeForce RTX 3090, 580.82.07, 8.6, 24576 MiB\n')"
smi_3090_550="$(fake_smi 3090 $'NVIDIA GeForce RTX 3090, 550.54.15, 8.6, 24576 MiB\n')"
smi_old="$(fake_smi old $'Field "compute_cap" is not a valid field to query.\n' $'Tesla T4, 470.256.02, 15109 MiB\n' 2)"
smi_broken="$(fake_smi broken $'NVIDIA-SMI has failed because it couldn\'t communicate with the NVIDIA driver.\n' $'NVIDIA-SMI has failed\n' 9 9)"
smi_na="$(fake_smi na $'NVIDIA A100-SXM4-80GB, 580.65.06, [N/A], 81920 MiB\n')"
smi_comma="$(fake_smi comma $'NVIDIA RTX A6000, Ada Generation, 580.65.06, 8.9, 49140 MiB\n')"
smi_empty="$(fake_smi empty $'No devices were found\n')"
no_smi="$tmp/does-not-exist/nvidia-smi"

# ---- 参数解析 ----

run_cmd bash "$install" --help
expect_code "help" 0
contains "help" "用法：install.sh --server-url URL"

run_cmd bash "$install" --prefix "$tmp/none"
expect_code "no server url" 2
contains "no server url" "缺少 --server-url"

run_cmd bash "$install" --server-url "$URL" --bogus
expect_code "unknown option" 2
contains "unknown option" "未知参数：--bogus"

run_cmd bash "$install" --server-url "$URL" --bogus=1
expect_code "unknown option=" 2
contains "unknown option=" "未知选项：--bogus=1"

run_cmd bash "$install" --server-url "$URL" positional
expect_code "positional" 2

run_cmd bash "$install" --server-url "$URL" --token
expect_code "missing value" 2
contains "missing value" "--token 需要一个参数"

run_cmd bash "$install" --server-url=
expect_code "empty value" 2

for bad_url in "10.0.0.5:8080" "ftp://10.0.0.5" "http://user:pw@10.0.0.5" "http://10.0.0.5/?a=1" "http://10.0.0.5/a b" "http://"; do
	dry "$no_smi" --server-url "$bad_url"
	expect_code "bad url $bad_url" 2
	contains "bad url $bad_url" "--server-url 不合法"
done

for bad_token in "with space" "中文令牌" "$(printf 'x%.0s' {1..257})"; do
	dry "$no_smi" --server-url "$URL" --token "$bad_token"
	expect_code "bad token" 2
	contains "bad token" "--token 只能包含可见的 ASCII 字符"
done

dry "$no_smi" --server-url "$URL" --name $'two\nlines'
expect_code "name with newline" 2

dry "$no_smi" --server-url "$URL" --prefix relative/dir
expect_code "relative prefix" 2
contains "relative prefix" "--prefix 必须是绝对路径"
dry "$no_smi" --server-url "$URL" --prefix "/opt/with space"
expect_code "prefix with space" 2
dry "$no_smi" --server-url "$URL" --prefix "/opt/100%"
expect_code "prefix with percent" 2
dry "$no_smi" --server-url "$URL" --prefix /
expect_code "root prefix" 2
dry "$no_smi" --server-url "$URL" --env-file "/etc/tm \"x\".env"
expect_code "env-file with quotes" 2
dry "$no_smi" --server-url "$URL" --state-dir var/lib/tm
expect_code "relative state dir" 2
contains "relative state dir" "--state-dir 必须是绝对路径"
dry "$no_smi" --server-url "$URL" --data-dir data
expect_code "relative data dir" 2

dry "$no_smi" --server-url "$URL" --variant cu999
expect_code "unknown variant" 2
contains "unknown variant" "未知的变体 cu999（可选：cu130 cu126 cu128 cu118 cpu）"

dry "$no_smi" --server-url "$URL" --user "bad user"
expect_code "bad user" 2

# ---- 显卡检测与变体选择 ----

token="tok-0123456789abcdef"
dry "$smi_4090" --server-url "$URL/" --token "$token" --name '机房 "A" $HOME `x` \n'
expect_code "4090" 0
contains "4090" "显卡：2 块"
contains "4090" "  [0] NVIDIA GeForce RTX 4090（显存 24564 MiB，计算能力 8.9）"
contains "4090" "  [1] NVIDIA GeForce RTX 3090（显存 24576 MiB，计算能力 8.6）"
contains "4090" "驱动：580.82.07"
contains "4090" "推荐的 PyTorch 变体：cu130 —— 驱动 580.82.07 支持 CUDA 13.0"
contains "4090" "+ python3 -m venv /opt/trainmaster-trainer"
contains "4090" "install-torch.sh cu130 --python /opt/trainmaster-trainer/bin/python"
# 没有设置 PIP_INDEX_URL：不传 --index-url，pip.conf 中的镜像仍然有效
contains "4090" "-m pip install torch==2.14.1 torchvision==0.29.1"
not_contains "4090" "--index-url"
contains "4090" "== 配置文件 /opt/trainmaster-trainer/trainer.env（权限 600）=="
contains "4090" 'TM_SERVER_URL="http://10.0.0.5:8080"'
contains "4090" 'TM_NODE_TOKEN="<已隐藏，共 20 个字符>"'
contains "4090" 'TM_NODE_NAME="机房 \"A\" \$HOME \`x\` \\n"'
contains "4090" 'TM_TORCH_VARIANT="cu130"'
contains "4090" 'TM_STATE_DIR="/var/lib/trainmaster"'
contains "4090" 'TM_DATA_DIR="/data"'
contains "4090" 'CUDA_DEVICE_ORDER="PCI_BUS_ID"'
contains "4090" "以上为预览（--dry-run），没有做任何修改。"
not_contains "4090" "$token"
not_contains "4090" "systemctl"

dry "$smi_3090_550" --server-url "$URL" --prefix /srv/tm
expect_code "3090 driver 550" 0
contains "3090 driver 550" "推荐的 PyTorch 变体：cu126"
contains "3090 driver 550" "-m pip install --extra-index-url https://download.pytorch.org/whl/cu126 torch==2.14.1+cu126 torchvision==0.29.1+cu126"
contains "3090 driver 550" "== 配置文件 /srv/tm/trainer.env"
contains "3090 driver 550" "+ /srv/tm/bin/python -m pip install --no-deps --force-reinstall $(dirname "$scripts")"
contains "3090 driver 550" 'TM_NODE_TOKEN=""'

dry "$smi_old" --server-url "$URL"
expect_code "old driver" 0
contains "old driver" "  [0] Tesla T4（显存 15109 MiB，计算能力 7.5）"
contains "old driver" "按 7.5 估计"
contains "old driver" "推荐的 PyTorch 变体：cu118"

dry "$smi_na" --server-url "$URL"
expect_code "cc N/A" 0
contains "cc N/A" "计算能力 7.5）"
contains "cc N/A" "按 7.5 估计"
contains "cc N/A" "推荐的 PyTorch 变体：cu130"

dry "$smi_comma" --server-url "$URL"
expect_code "name with comma" 0
contains "name with comma" "  [0] NVIDIA RTX A6000, Ada Generation（显存 49140 MiB，计算能力 8.9）"
contains "name with comma" "驱动：580.65.06"

dry "$smi_broken" --server-url "$URL"
expect_code "broken driver" 0
contains "broken driver" "显卡：无法检测（nvidia-smi 运行失败：NVIDIA-SMI has failed"
contains "broken driver" "推荐的 PyTorch 变体：cpu"

dry "$smi_empty" --server-url "$URL"
expect_code "no devices" 0
contains "no devices" "nvidia-smi 没有列出显卡：No devices were found"
contains "no devices" 'TM_TORCH_VARIANT="cpu"'

dry "$no_smi" --server-url "$URL"
expect_code "no nvidia-smi" 0
contains "no nvidia-smi" "显卡：未检测到 NVIDIA 显卡（找不到 nvidia-smi）"
contains "no nvidia-smi" "推荐的 PyTorch 变体：cpu —— 未检测到 NVIDIA 显卡"
contains "no nvidia-smi" "torch==2.14.1+cpu torchvision==0.29.1+cpu"

MACHINE=aarch64 dry "$smi_4090" --server-url "$URL"
expect_code "arm" 0
contains "arm" "平台：aarch64"
contains "arm" "推荐的 PyTorch 变体：cu130 —— ARM 服务器"

dry "$smi_4090" --server-url "$URL" --variant cpu
expect_code "manual cpu" 0
contains "manual cpu" "推荐的 PyTorch 变体：cu130"
contains "manual cpu" "使用手动指定的变体：cpu"
contains "manual cpu" "训练不会使用显卡"
contains "manual cpu" "install-torch.sh cpu"

dry "$smi_4090" --server-url="$URL" --variant=cu128 --name=gpu-01 --state-dir=/srv/state --data-dir=/srv/data --env-file=/etc/trainmaster.env
expect_code "key=value" 0
contains "key=value" "使用手动指定的变体：cu128"
contains "key=value" 'TM_NODE_NAME="gpu-01"'
contains "key=value" 'TM_STATE_DIR="/srv/state"'
contains "key=value" 'TM_DATA_DIR="/srv/data"'
contains "key=value" "== 配置文件 /etc/trainmaster.env"
contains "key=value" "+ mkdir -p /srv/state"

UNIT_DIR=/etc/systemd/system dry "$no_smi" --server-url "$URL" --systemd --env-file /etc/trainmaster.env
expect_code "systemd" 0
contains "systemd" "== systemd 服务 /etc/systemd/system/trainmaster-trainer.service =="
contains "systemd" "EnvironmentFile=/etc/trainmaster.env"
contains "systemd" "ExecStart=/opt/trainmaster-trainer/bin/python -m trainmaster_trainer run"
contains "systemd" "Restart=always"
contains "systemd" "WantedBy=multi-user.target"
contains "systemd" "+ systemctl daemon-reload"
contains "systemd" "+ systemctl enable trainmaster-trainer.service"
contains "systemd" "+ systemctl restart trainmaster-trainer.service"
not_contains "systemd" "User="

dry "$no_smi" --server-url "$URL" --systemd --user svc-train
expect_code "systemd user" 0
contains "systemd user" "User=svc-train"
contains "systemd user" "+ chown svc-train: /var/lib/trainmaster"

# ---- 重新运行：沿用已有的配置文件与 systemd 服务 ----

existing_env="$tmp/existing.env"
cat >"$existing_env" <<'EOF'
# 旧的注释
TM_SERVER_URL="http://10.0.0.9:8080"
TM_NODE_TOKEN="tok-\$abc\"def"
TM_NODE_NAME='机房 B'
TM_TORCH_VARIANT="cu126"
TM_STATE_DIR=/srv/tm-state
TM_DATA_DIR="/srv/tm-data"
export TM_LOG_LEVEL=debug
CUDA_VISIBLE_DEVICES="0"
EOF
dry "$no_smi" --env-file "$existing_env" --variant cu128
expect_code "reuse env" 0
contains "reuse env" "沿用配置文件 $existing_env 中的 TM_SERVER_URL TM_NODE_TOKEN TM_NODE_NAME TM_STATE_DIR TM_DATA_DIR"
contains "reuse env" 'TM_SERVER_URL="http://10.0.0.9:8080"'
contains "reuse env" 'TM_NODE_TOKEN="<已隐藏，共 12 个字符>"'
contains "reuse env" 'TM_NODE_NAME="机房 B"'
contains "reuse env" 'TM_TORCH_VARIANT="cu128"'
contains "reuse env" 'TM_STATE_DIR="/srv/tm-state"'
contains "reuse env" 'TM_DATA_DIR="/srv/tm-data"'
contains "reuse env" 'CUDA_DEVICE_ORDER="PCI_BUS_ID"'
contains "reuse env" $'\nTM_LOG_LEVEL=debug\nCUDA_VISIBLE_DEVICES="0"'
contains "reuse env" "+ mkdir -p /srv/tm-state"
not_contains "reuse env" "旧的注释"
not_contains "reuse env" "systemctl"

# 参数优先于配置文件
dry "$no_smi" --env-file "$existing_env" --server-url "$URL" --name gpu-02 --state-dir /srv/other
contains "reuse override" "沿用配置文件 $existing_env 中的 TM_NODE_TOKEN TM_DATA_DIR"
contains "reuse override" 'TM_SERVER_URL="http://10.0.0.5:8080"'
contains "reuse override" 'TM_NODE_NAME="gpu-02"'
contains "reuse override" 'TM_STATE_DIR="/srv/other"'

# 配置文件中没有平台地址时仍然需要 --server-url
printf 'TM_NODE_TOKEN="x"\n' >"$tmp/no-url.env"
dry "$no_smi" --env-file "$tmp/no-url.env"
expect_code "reuse without url" 2
contains "reuse without url" "缺少 --server-url"

# 已安装本安装目录的 systemd 服务：不加 --systemd 也更新并重启它，沿用运行用户
mkdir -p "$tmp/units-default" "$tmp/units-other"
printf '[Service]\nUser=svc-train\nEnvironmentFile=/opt/trainmaster-trainer/trainer.env\nExecStart=/opt/trainmaster-trainer/bin/python -m trainmaster_trainer run\n' \
	>"$tmp/units-default/trainmaster-trainer.service"
UNIT_DIR="$tmp/units-default" dry "$no_smi" --server-url "$URL"
expect_code "systemd found" 0
contains "systemd found" "已安装 systemd 服务 $tmp/units-default/trainmaster-trainer.service：安装后更新并重启它"
contains "systemd found" "User=svc-train"
contains "systemd found" "+ systemctl restart trainmaster-trainer.service"
# 加上 --systemd 时同样沿用运行用户；--user 优先
UNIT_DIR="$tmp/units-default" dry "$no_smi" --server-url "$URL" --systemd
expect_code "systemd found explicit" 0
contains "systemd found explicit" "User=svc-train"
not_contains "systemd found explicit" "已安装 systemd 服务"
UNIT_DIR="$tmp/units-default" dry "$no_smi" --server-url "$URL" --user other-user
contains "systemd found user override" "User=other-user"
not_contains "systemd found user override" "User=svc-train"
# 首次安装用 --env-file 指定了其它配置文件：重新运行时从已安装的服务中找到它
mkdir -p "$tmp/units-envfile"
printf '[Service]\nEnvironmentFile=%s\nExecStart=/opt/trainmaster-trainer/bin/python -m trainmaster_trainer run\n' \
	"$existing_env" >"$tmp/units-envfile/trainmaster-trainer.service"
UNIT_DIR="$tmp/units-envfile" dry "$no_smi" --variant cu128
expect_code "unit env file" 0
contains "unit env file" "沿用配置文件 $existing_env 中的 TM_SERVER_URL TM_NODE_TOKEN"
contains "unit env file" "== 配置文件 $existing_env（权限 600）=="
contains "unit env file" "EnvironmentFile=$existing_env"
contains "unit env file" "+ systemctl restart trainmaster-trainer.service"
# 其它安装目录的服务不受影响
printf '[Service]\nExecStart=/srv/other/bin/python -m trainmaster_trainer run\n' >"$tmp/units-other/trainmaster-trainer.service"
UNIT_DIR="$tmp/units-other" dry "$no_smi" --server-url "$URL"
expect_code "other unit" 0
not_contains "other unit" "systemctl"
not_contains "other unit" "已安装 systemd 服务"

# ---- install-torch.sh --dry-run ----

torch_dry() { # torch_dry <参数…>
	run_cmd env -u PIP_INDEX_URL -u TORCH_INDEX_URL -u TORCH_FIND_LINKS "$@"
}

# 没有设置 PIP_INDEX_URL 时不传 --index-url（命令行参数会覆盖 pip.conf 中的镜像）
torch_dry bash "$install_torch" cu130 --python py --dry-run
expect_code "torch cu130" 0
contains "torch cu130" "+ py -m pip install torch==2.14.1 torchvision==0.29.1"

torch_dry PIP_INDEX_URL=https://mirrors.example.com/pypi/simple bash "$install_torch" cu130 --python py --dry-run
contains "torch pip mirror" "+ py -m pip install --index-url https://mirrors.example.com/pypi/simple torch==2.14.1"

# PyTorch 索引作为额外索引：pip 配置的索引仍是主索引
torch_dry bash "$install_torch" cu118 --python=py --dry-run
contains "torch cu118" "+ py -m pip install --extra-index-url https://download.pytorch.org/whl/cu118 torch==2.7.1+cu118 torchvision==0.22.1+cu118"

torch_dry PIP_INDEX_URL=https://mirrors.example.com/pypi/simple bash "$install_torch" cu126 --python py --dry-run
contains "torch cu126 pip mirror" "+ py -m pip install --index-url https://download.pytorch.org/whl/cu126 --extra-index-url https://mirrors.example.com/pypi/simple torch==2.14.1+cu126"

torch_dry TORCH_INDEX_URL=https://torch.example.com/whl/ bash "$install_torch" cu128 --python py --dry-run
contains "torch index" "+ py -m pip install --extra-index-url https://torch.example.com/whl/cu128 torch==2.11.0+cu128 torchvision==0.26.0+cu128"

torch_dry TORCH_FIND_LINKS=https://mirrors.example.com/pytorch-wheels/ PIP_INDEX_URL=https://mirrors.example.com/pypi/simple bash "$install_torch" cu126 --python py --dry-run
contains "torch find-links" "--index-url https://mirrors.example.com/pypi/simple --find-links https://mirrors.example.com/pytorch-wheels/cu126/ torch==2.14.1+cu126 torchvision==0.29.1+cu126"

torch_dry TORCH_FIND_LINKS=https://mirrors.example.com/pytorch-wheels bash "$install_torch" cu126 --python py --dry-run
contains "torch find-links without pip mirror" "+ py -m pip install --find-links https://mirrors.example.com/pytorch-wheels/cu126/ torch==2.14.1+cu126"

torch_dry bash "$install_torch" cpu --python py --dry-run
contains "torch cpu" "（变体 cpu，CUDA 无）"
contains "torch cpu" "torch==2.14.1+cpu torchvision==0.29.1+cpu"

torch_dry bash "$install_torch" --dry-run
expect_code "torch no variant" 2
contains "torch no variant" "缺少变体名"
torch_dry bash "$install_torch" cu999 --dry-run
expect_code "torch unknown variant" 2
torch_dry bash "$install_torch" cu130 cu126 --dry-run
expect_code "torch two variants" 2
torch_dry bash "$install_torch" cu130 --python
expect_code "torch python without value" 2
torch_dry bash "$install_torch" cu130 --bogus
expect_code "torch unknown option" 2
torch_dry bash "$install_torch" --help
expect_code "torch help" 0

# ---- 实际安装（假的 python / pip / systemctl）----

# 假 python：记录调用；-m venv 复制自身；-m pip install 记录装上的 torch；"-" 输出已安装的 torch 信息。
cat >"$tmp/bin/fakepy" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_LOG"
case "$1" in
-c) exit 0 ;;
-) cat >/dev/null; [ -f "$FAKE_TORCH" ] && cat "$FAKE_TORCH" && exit 0; exit 1 ;;
-m)
	case "$2" in
	venv) mkdir -p "$3/bin" && cp "$0" "$3/bin/python" && exit 0 ;;
	pip)
		for arg in "$@"; do
			case "$arg" in
			torch==*) torch="${arg#torch==}" ;;
			torchvision==*) vision="${arg#torchvision==}" ;;
			esac
		done
		if [ -n "${torch:-}" ]; then
			printf '%s\n%s\n%s\n' "$torch" "${FAKE_CUDA:-None}" "$vision" >"$FAKE_TORCH"
		fi
		exit 0
		;;
	trainmaster_trainer) echo "CUDA 环境：CPU 模式（没有可用的 GPU）"; exit "${FAKE_SELFTEST:-0}" ;;
	esac
	;;
esac
exit 0
EOF
chmod +x "$tmp/bin/fakepy"
cat >"$tmp/bin/systemctl" <<'EOF'
#!/bin/sh
echo "systemctl $*" >>"$FAKE_LOG"
EOF
chmod +x "$tmp/bin/systemctl"

real() { # real <prefix 名> <参数…>：不带 --dry-run 运行 install.sh
	local name="$1"
	shift
	run_cmd env PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/$name.log" FAKE_TORCH="$tmp/$name.torch" \
		TM_NVIDIA_SMI="$no_smi" TM_MACHINE=x86_64 TM_SYSTEMD_UNIT_DIR="$tmp/units" \
		bash "$install" --python "$tmp/bin/fakepy" --prefix "$tmp/$name" \
		--state-dir "$tmp/$name-state" --data-dir "$tmp" "$@"
}

mkdir -p "$tmp/units"
weird_name='机房 "A" $HOME `x` \n'
real venv1 --server-url "$URL" --token "$token" --name "$weird_name" --systemd --user "$(id -un)"
expect_code "real install" 0
contains "real install" "安装完成：PyTorch 变体 cpu，自检通过。"
contains "real install" "install-torch: 已安装 torch 2.14.1+cpu（CUDA 无）、torchvision 0.29.1+cpu"
contains "real install" "训练节点已作为 systemd 服务运行"
[ -x "$tmp/venv1/bin/python" ] || fail "real install: venv not created"
[ -d "$tmp/venv1-state" ] || fail "real install: state dir not created"
check "env file mode" 600 "$(stat -c %a "$tmp/venv1/trainer.env")"
# 用 shell 读取配置文件，值与输入完全一致
env_value() { # env_value <文件> <变量>：用 shell 读取配置文件中的值
	(
		# shellcheck source=/dev/null
		. "$1"
		printf '%s' "${!2}"
	)
}
check "env name" "$weird_name" "$(env_value "$tmp/venv1/trainer.env" TM_NODE_NAME)"
check "env token" "$token" "$(env_value "$tmp/venv1/trainer.env" TM_NODE_TOKEN)"
check "env url" "$URL" "$(env_value "$tmp/venv1/trainer.env" TM_SERVER_URL)"
check "env variant" cpu "$(env_value "$tmp/venv1/trainer.env" TM_TORCH_VARIANT)"
check "env state dir" "$tmp/venv1-state" "$(env_value "$tmp/venv1/trainer.env" TM_STATE_DIR)"
check "env cuda order" PCI_BUS_ID "$(env_value "$tmp/venv1/trainer.env" CUDA_DEVICE_ORDER)"
unit="$tmp/units/trainmaster-trainer.service"
[ -f "$unit" ] || fail "real install: unit file not written"
check "unit mode" 644 "$(stat -c %a "$unit")"
grep -qx "EnvironmentFile=$tmp/venv1/trainer.env" "$unit" || fail "unit EnvironmentFile"
grep -qx "ExecStart=$tmp/venv1/bin/python -m trainmaster_trainer run" "$unit" || fail "unit ExecStart"
log="$(cat "$tmp/venv1.log")"
for expected in "-m venv $tmp/venv1" "-m pip install --upgrade pip" "-m pip install -r $(dirname "$scripts")/requirements.txt" \
	"-m pip install --no-deps --force-reinstall $(dirname "$scripts")" "-m trainmaster_trainer selftest" \
	"systemctl daemon-reload" "systemctl enable trainmaster-trainer.service" "systemctl restart trainmaster-trainer.service"; do
	[[ "$log" == *"$expected"* ]] || fail "real install: missing call [$expected]"
done
[ -z "$(find "$tmp/venv1" "$tmp/units" -name '.*' -newer "$tmp/bin/fakepy")" ] || fail "real install: temporary files left behind"

# 再次运行（训练节点修复建议中的命令）：只指定变体。复用虚拟环境，变体不变时不卸载 torch；
# 平台地址、令牌、名称、目录与手动添加的设置沿用，已安装的 systemd 服务更新并重启。
printf 'TM_LOG_LEVEL="debug"\nexport CUDA_VISIBLE_DEVICES=0\n' >>"$tmp/venv1/trainer.env"
rerun() {
	: >"$tmp/venv1.log"
	run_cmd env PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/venv1.log" FAKE_TORCH="$tmp/venv1.torch" \
		TM_NVIDIA_SMI="$no_smi" TM_MACHINE=x86_64 TM_SYSTEMD_UNIT_DIR="$tmp/units" \
		bash "$install" --prefix "$tmp/venv1" --variant cpu
}
rerun
expect_code "rerun" 0
contains "rerun" "复用已有的虚拟环境 $tmp/venv1"
contains "rerun" "沿用配置文件 $tmp/venv1/trainer.env 中的 TM_SERVER_URL TM_NODE_TOKEN TM_NODE_NAME TM_STATE_DIR TM_DATA_DIR"
contains "rerun" "已安装 systemd 服务 $tmp/units/trainmaster-trainer.service：安装后更新并重启它"
contains "rerun" "训练节点已作为 systemd 服务运行"
not_contains "rerun" "卸载已安装的 torch"
not_contains "rerun" "$token"
check "rerun token" "$token" "$(env_value "$tmp/venv1/trainer.env" TM_NODE_TOKEN)"
check "rerun name" "$weird_name" "$(env_value "$tmp/venv1/trainer.env" TM_NODE_NAME)"
check "rerun url" "$URL" "$(env_value "$tmp/venv1/trainer.env" TM_SERVER_URL)"
check "rerun state dir" "$tmp/venv1-state" "$(env_value "$tmp/venv1/trainer.env" TM_STATE_DIR)"
check "rerun data dir" "$tmp" "$(env_value "$tmp/venv1/trainer.env" TM_DATA_DIR)"
check "rerun log level" debug "$(env_value "$tmp/venv1/trainer.env" TM_LOG_LEVEL)"
check "rerun visible devices" 0 "$(env_value "$tmp/venv1/trainer.env" CUDA_VISIBLE_DEVICES)"
check "rerun env file mode" 600 "$(stat -c %a "$tmp/venv1/trainer.env")"
grep -qx "systemctl restart trainmaster-trainer.service" "$tmp/venv1.log" || fail "rerun: service not restarted"
first="$(cat "$tmp/venv1/trainer.env")"
rerun
check "rerun is stable" "$first" "$(cat "$tmp/venv1/trainer.env")"

# 自检失败不影响安装结果，但给出提示
FAKE_SELFTEST=1 real venv1 --server-url "$URL"
expect_code "selftest failed" 0
contains "selftest failed" "安装完成，但自检发现问题"

# 装上的 torch 与变体不一致（例如镜像源给了 CUDA 版）时失败；其它安装目录不会接管已有的 systemd 服务
FAKE_CUDA=12.6 real venv2 --server-url "$URL"
expect_code "torch mismatch" 1
contains "torch mismatch" "安装结果与变体 cpu 不一致"
not_contains "torch mismatch" "已安装 systemd 服务"

# 安装目录已存在但不是虚拟环境
mkdir -p "$tmp/notvenv"
echo data >"$tmp/notvenv/file"
real notvenv --server-url "$URL"
expect_code "prefix not a venv" 1
contains "prefix not a venv" "已存在且不是虚拟环境"

# --systemd 但服务目录不可写
run_cmd env PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/x.log" TM_NVIDIA_SMI="$no_smi" TM_SYSTEMD_UNIT_DIR="$tmp/missing-units" \
	bash "$install" --python "$tmp/bin/fakepy" --prefix "$tmp/venv3" --server-url "$URL" --systemd
expect_code "systemd without unit dir" 1
contains "systemd without unit dir" "--systemd 需要 root 权限"
[ ! -e "$tmp/venv3" ] || fail "systemd without unit dir: installed before checking"

run_cmd env TM_NVIDIA_SMI="$no_smi" bash "$install" --python "$tmp/does-not-exist/python3" --prefix "$tmp/venv4" --server-url "$URL"
expect_code "missing python" 1
contains "missing python" "找不到 Python"

run_cmd env TM_NVIDIA_SMI="$no_smi" bash "$install" --prefix "$tmp/venv5" --server-url "$URL" --user no-such-user-tm
expect_code "missing user" 1
contains "missing user" "用户 no-such-user-tm 不存在"

if [ "$fails" -gt 0 ]; then
	echo "$fails test(s) failed" >&2
	exit 1
fi
echo "install.sh / install-torch.sh: all tests passed"
