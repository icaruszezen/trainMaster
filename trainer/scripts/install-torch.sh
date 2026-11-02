#!/usr/bin/env bash
# 按 PyTorch 变体表（trainmaster_trainer/torch_variants.txt）安装 torch / torchvision，
# 并验证装上的确实是该变体：torch.version.cuda 与表中的 cuda 一致（cpu 变体为 None）。
# 镜像构建（trainer/Dockerfile）与裸机安装（trainer/scripts/install.sh）共用。
#
# 用法：install-torch.sh <variant> [--python PY] [--dry-run]
#   variant    变体名：cu130、cu126、cu128、cu118、cpu（见变体表）
#   --python   安装到哪个 Python（默认 python3，没有时用 python）
#   --dry-run  只打印将要执行的 pip 命令，不安装也不验证
#
# 环境变量：
#   PIP_INDEX_URL     PyPI 或其镜像，默认 https://pypi.org/simple。source=pypi 的变体与所有依赖从这里安装
#   TORCH_INDEX_URL   PyTorch 官方索引（PEP 503）的基础地址，默认 https://download.pytorch.org/whl，
#                     实际使用 <TORCH_INDEX_URL>/<variant>
#   TORCH_FIND_LINKS  扁平目录形式的 PyTorch 镜像基础地址（如 https://mirrors.aliyun.com/pytorch-wheels），
#                     设置后代替 TORCH_INDEX_URL，实际使用 <TORCH_FIND_LINKS>/<variant>/
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=SCRIPTDIR/lib/torch-variant.sh
. "$script_dir/lib/torch-variant.sh"

usage() {
	cat <<'EOF'
用法：install-torch.sh <variant> [--python PY] [--dry-run]
  variant    变体名：cu130、cu126、cu128、cu118、cpu（见 trainmaster_trainer/torch_variants.txt）
  --python   安装到哪个 Python（默认 python3，没有时用 python）
  --dry-run  只打印将要执行的 pip 命令，不安装也不验证
EOF
}

die() { # die <退出码> <消息>
	echo "install-torch: $2" >&2
	exit "$1"
}

variant=""
python=""
dry_run=0
while [ $# -gt 0 ]; do
	case "$1" in
	--python)
		if [ $# -lt 2 ] || [ -z "$2" ]; then
			die 2 "--python 需要一个参数"
		fi
		python="$2"
		shift 2
		;;
	--python=*)
		python="${1#--python=}"
		[ -n "$python" ] || die 2 "--python 需要一个参数"
		shift
		;;
	--dry-run)
		dry_run=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		usage >&2
		die 2 "未知选项：$1"
		;;
	*)
		[ -z "$variant" ] || die 2 "只能指定一个变体（已指定 $variant，又指定了 $1）"
		variant="$1"
		shift
		;;
	esac
done

if [ -z "$variant" ]; then
	usage >&2
	die 2 "缺少变体名（可选：$(tm_variant_names | tr '\n' ' ' | sed 's/ $//')）"
fi
if ! torch_version="$(tm_variant_field "$variant" torch)"; then
	die 2 "未知的变体 $variant（可选：$(tm_variant_names | tr '\n' ' ' | sed 's/ $//')）"
fi
torchvision_version="$(tm_variant_field "$variant" torchvision)"
source="$(tm_variant_field "$variant" source)"
cuda="$(tm_variant_field "$variant" cuda)"
expected_cuda="$cuda"
[ "$cuda" = "-" ] && expected_cuda="None"

if [ -z "$python" ]; then
	if command -v python3 >/dev/null 2>&1; then
		python=python3
	else
		python=python
	fi
fi

pip_index="${PIP_INDEX_URL:-https://pypi.org/simple}"
torch_index="${TORCH_INDEX_URL:-https://download.pytorch.org/whl}"
find_links="${TORCH_FIND_LINKS:-}"

pip_cmd=("$python" -m pip install)
case "$source" in
pypi)
	# PyPI 上的 Linux 版 torch 就是该 CUDA 版本的构建，版本号不带后缀。
	pip_cmd+=(--index-url "$pip_index" "torch==$torch_version" "torchvision==$torchvision_version")
	;;
pytorch)
	if [ -n "$find_links" ]; then
		# 扁平目录镜像只有 torch / torchvision 的 wheel，其余依赖（nvidia-* 运行库等）从 PIP_INDEX_URL 安装。
		pip_cmd+=(--index-url "$pip_index" --find-links "${find_links%/}/$variant/")
	else
		# 官方索引也提供全部依赖；加上 PIP_INDEX_URL 作为备用，镜像索引缺少某个依赖时也能装上。
		pip_cmd+=(--index-url "${torch_index%/}/$variant" --extra-index-url "$pip_index")
	fi
	pip_cmd+=("torch==$torch_version+$variant" "torchvision==$torchvision_version+$variant")
	;;
*)
	die 1 "变体表中 $variant 的 source 列不正确：$source（应为 pypi 或 pytorch）"
	;;
esac

echo "install-torch: 安装 PyTorch $torch_version / torchvision $torchvision_version（变体 $variant，CUDA ${cuda/#-/无}）"

if [ "$dry_run" = 1 ]; then
	printf '+'
	printf ' %q' "${pip_cmd[@]}"
	printf '\n'
	exit 0
fi

command -v "$python" >/dev/null 2>&1 || die 1 "找不到 Python 解释器：$python"
"$python" -m pip --version >/dev/null 2>&1 || die 1 "$python 没有可用的 pip（Debian / Ubuntu 可安装 python3-pip 或 python3-venv）"

# 输出已安装的 torch 版本、torch.version.cuda 与 torchvision 版本（各占一行）；没有安装时返回非 0。
installed_info() {
	"$python" - <<'EOF' 2>/dev/null
import torch
import torchvision

print(torch.__version__)
print(torch.version.cuda)
print(torchvision.__version__)
EOF
}

# matches <torch 版本> <cuda> <torchvision 版本>：与本变体一致时返回 0（忽略版本号的 +后缀）。
matches() {
	[ "${1%%+*}" = "$torch_version" ] && [ "$2" = "$expected_cuda" ] && [ "${3%%+*}" = "$torchvision_version" ]
}

# pip 认为 2.14.1+cu126 满足 torch==2.14.1，从其它变体切换到 source=pypi 的变体时不会重新安装，
# 所以已安装的版本或 CUDA 不一致时先卸载。
if info="$(installed_info)"; then
	mapfile -t current <<<"$info"
	if ! matches "${current[0]:-}" "${current[1]:-}" "${current[2]:-}"; then
		echo "install-torch: 卸载已安装的 torch ${current[0]:-?}（CUDA ${current[1]:-?}）/ torchvision ${current[2]:-?}"
		"$python" -m pip uninstall -y torch torchvision
	fi
fi

printf '+'
printf ' %q' "${pip_cmd[@]}"
printf '\n'
"${pip_cmd[@]}"

if ! info="$(installed_info)"; then
	die 1 "安装完成后无法导入 torch / torchvision，请查看上面 pip 的输出"
fi
mapfile -t current <<<"$info"
got_torch="${current[0]:-}"
got_cuda="${current[1]:-}"
got_vision="${current[2]:-}"
if ! matches "$got_torch" "$got_cuda" "$got_vision"; then
	echo "install-torch: 安装结果与变体 $variant 不一致：" >&2
	echo "  torch $got_torch（应为 $torch_version），torch.version.cuda 为 $got_cuda（应为 $expected_cuda），torchvision $got_vision（应为 $torchvision_version）" >&2
	echo "  通常是镜像源中没有对应的构建、pip 改装了其它版本。请检查 PIP_INDEX_URL、TORCH_INDEX_URL、TORCH_FIND_LINKS 的设置。" >&2
	exit 1
fi
echo "install-torch: 已安装 torch $got_torch（CUDA ${got_cuda/#None/无}）、torchvision $got_vision"
