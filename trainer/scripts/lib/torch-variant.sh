# shellcheck shell=bash disable=SC2034  # TM_VARIANT* 等全局变量供调用方读取
# 按 NVIDIA 驱动版本与显卡计算能力选择 PyTorch 构建变体（cu130 / cu126 / cu128 / cu118 / cpu）。
#
# 用法（bash）：
#   . trainer/scripts/lib/torch-variant.sh
#   tm_select_variant "550.54.15" "86 89" x86_64
#   echo "$TM_VARIANT"              # cu126
#   echo "$TM_VARIANT_REASON"       # 选择原因（中文，可直接展示给用户）
#   echo "$TM_VARIANT_UNSUPPORTED"  # 所选变体不支持的计算能力（空格分隔），通常为空
#
# 数据来自 trainmaster_trainer/torch_variants.txt（可用 TM_TORCH_VARIANTS_FILE 指定其他路径），
# 训练节点的 Python 代码（trainmaster_trainer.variants）使用同一份数据和同样的规则。

_tm_variant_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 变体表文件路径。
tm_variants_file() {
	if [ -n "${TM_TORCH_VARIANTS_FILE:-}" ]; then
		printf '%s\n' "$TM_TORCH_VARIANTS_FILE"
	else
		printf '%s\n' "$_tm_variant_lib_dir/../../trainmaster_trainer/torch_variants.txt"
	fi
}

# 列出表中的变体名（按优先顺序）。
tm_variant_names() {
	awk '!/^[[:space:]]*(#|$)/ { print $1 }' "$(tm_variants_file)"
}

# tm_variant_field <variant> <field>：输出某个变体的一列。
# field 为 torch、torchvision、source、cuda、min_driver、archs、min_driver_win 之一；变体不存在时返回 1。
tm_variant_field() {
	local variant="$1" field="$2" col
	case "$field" in
	torch) col=2 ;;
	torchvision) col=3 ;;
	source) col=4 ;;
	cuda) col=5 ;;
	min_driver) col=6 ;;
	archs) col=7 ;;
	min_driver_win) col=8 ;;
	*)
		echo "tm_variant_field: 未知的列 $field" >&2
		return 2
		;;
	esac
	awk -v v="$variant" -v c="$col" '
		!/^[[:space:]]*(#|$)/ && $1 == v { print $c; found = 1; exit }
		END { exit found ? 0 : 1 }
	' "$(tm_variants_file)"
}

# tm_version_ge <a> <b>：点分数字版本 a >= b 时返回 0（缺少的部分按 0 处理，如 580 = 580.0.0）。
tm_version_ge() {
	awk -v a="$1" -v b="$2" 'BEGIN {
		na = split(a, x, "."); nb = split(b, y, ".")
		n = na > nb ? na : nb
		for (i = 1; i <= n; i++) {
			xi = (i <= na) ? x[i] + 0 : 0
			yi = (i <= nb) ? y[i] + 0 : 0
			if (xi > yi) exit 0
			if (xi < yi) exit 1
		}
		exit 0
	}'
}

# tm_cc_to_int <cc>：把 nvidia-smi 的计算能力（如 8.6、12.0）转换为 86、120；无法解析时返回 1。
tm_cc_to_int() {
	local cc="$1"
	case "$cc" in
	[0-9]*.[0-9]*)
		local major="${cc%%.*}" minor="${cc#*.}"
		minor="${minor%%.*}"
		case "$major$minor" in *[!0-9]*) return 1 ;; esac
		printf '%d\n' "$((10#$major * 10 + 10#${minor:0:1}))"
		;;
	*) return 1 ;;
	esac
}

# tm_cc_covered <device_cc> <archs>：显卡计算能力（整数，如 89）能运行 archs（逗号分隔）中的某个构建时返回 0。
# 规则与 PyTorch 相同：同一主版本、次版本号不小于构建的次版本号；Jetson 集成显卡（5.3、6.2、7.2、8.7）
# 只能运行为它单独编译的代码。
tm_cc_covered() {
	local device="$1" archs="$2" arch
	[ "$archs" = "-" ] && return 1
	local IFS=,
	for arch in $archs; do
		[ -n "$arch" ] || continue
		if [ "$device" = "$arch" ]; then
			return 0
		fi
		case "$device" in 53 | 62 | 72 | 87) continue ;; esac
		case "$arch" in 53 | 62 | 72 | 87) continue ;; esac
		if [ $((device / 10)) -eq $((arch / 10)) ] && [ $((device % 10)) -ge $((arch % 10)) ]; then
			return 0
		fi
	done
	return 1
}

# tm_select_variant <driver_version> <ccs> [machine]
#   driver_version：NVIDIA 驱动版本（如 550.54.15），没有驱动时为空
#   ccs：各显卡的计算能力（整数或 8.6 形式，空格分隔），没有显卡时为空
#   machine：uname -m（默认取当前机器；不区分大小写）
# 设置 TM_VARIANT、TM_VARIANT_REASON、TM_VARIANT_UNSUPPORTED。总是返回 0。
tm_select_variant() {
	local driver="$1" raw_ccs="$2" machine="${3:-$(uname -m)}"
	machine="${machine,,}"
	TM_VARIANT=cpu
	TM_VARIANT_REASON=""
	TM_VARIANT_UNSUPPORTED=""

	local ccs="" cc n
	for cc in $raw_ccs; do
		if n="$(tm_cc_to_int "$cc" 2>/dev/null)"; then
			ccs="$ccs $n"
		elif [[ "$cc" =~ ^[0-9]+$ ]]; then
			ccs="$ccs $cc"
		fi
	done
	ccs="${ccs# }"

	if [ -z "$ccs" ]; then
		TM_VARIANT_REASON="未检测到 NVIDIA 显卡，使用 CPU 版 PyTorch"
		return 0
	fi
	if [ -z "$driver" ]; then
		TM_VARIANT_REASON="检测到 NVIDIA 显卡，但没有可用的驱动（nvidia-smi 无法运行），使用 CPU 版 PyTorch"
		return 0
	fi

	case "$machine" in
	x86_64 | amd64) ;;
	aarch64 | arm64)
		# ARM 服务器（Grace Hopper、GB10 等）只有 CUDA 13 的通用构建（sbsa），需要 Ampere 及更新的显卡。
		local ok=1
		for cc in $ccs; do
			[ "$cc" -ge 80 ] || ok=0
		done
		local min
		min="$(tm_variant_field cu130 min_driver)"
		if [ "$ok" = 1 ] && tm_version_ge "$driver" "$min"; then
			TM_VARIANT=cu130
			TM_VARIANT_REASON="ARM 服务器：驱动 $driver 支持 CUDA 13，使用 PyPI 上的 CUDA 13 版 PyTorch"
		else
			TM_VARIANT_REASON="ARM 服务器只能使用 CUDA 13 版 PyTorch（需要驱动 ≥ $min 且显卡为 Ampere 或更新），当前不满足，使用 CPU 版"
		fi
		return 0
		;;
	*)
		TM_VARIANT_REASON="不支持的平台 $machine，使用 CPU 版 PyTorch"
		return 0
		;;
	esac

	local total=0
	for cc in $ccs; do total=$((total + 1)); done

	local variant best="" best_cov=0 min archs cuda covered unsupported best_unsupported="" driver_ok_any=0
	for variant in $(tm_variant_names); do
		[ "$variant" = cpu ] && continue
		min="$(tm_variant_field "$variant" min_driver)"
		tm_version_ge "$driver" "$min" || continue
		driver_ok_any=1
		archs="$(tm_variant_field "$variant" archs)"
		covered=0
		unsupported=""
		for cc in $ccs; do
			if tm_cc_covered "$cc" "$archs"; then
				covered=$((covered + 1))
			else
				unsupported="$unsupported $cc"
			fi
		done
		cuda="$(tm_variant_field "$variant" cuda)"
		if [ "$covered" -eq "$total" ]; then
			TM_VARIANT="$variant"
			TM_VARIANT_REASON="驱动 $driver 支持 CUDA $cuda，PyTorch $(tm_variant_field "$variant" torch)（$variant）支持全部 $total 块显卡"
			return 0
		fi
		if [ "$covered" -gt "$best_cov" ]; then
			best="$variant"
			best_cov="$covered"
			best_unsupported="${unsupported# }"
		fi
	done

	if [ -n "$best" ]; then
		TM_VARIANT="$best"
		TM_VARIANT_UNSUPPORTED="$best_unsupported"
		TM_VARIANT_REASON="没有同时支持全部显卡的 PyTorch 版本，选择支持最多显卡的 $best（$best_cov/$total 块）；计算能力为 $(tm_cc_list "$best_unsupported") 的显卡无法用于训练"
		return 0
	fi

	if [ "$driver_ok_any" = 0 ]; then
		TM_VARIANT_REASON="驱动 $driver 过旧：CUDA 版 PyTorch 至少需要驱动 $(tm_variant_field cu118 min_driver)（推荐升级到 580 或更新），使用 CPU 版"
	else
		TM_VARIANT_REASON="显卡（计算能力 $(tm_cc_list "$ccs")）过旧，没有支持它的 PyTorch 版本，使用 CPU 版"
	fi
	return 0
}

# tm_cc_list "86 120"：输出 "8.6、12.0"，用于提示文字。
tm_cc_list() {
	local out="" cc
	for cc in $1; do
		out="$out、$((cc / 10)).$((cc % 10))"
	done
	printf '%s' "${out#、}"
}
