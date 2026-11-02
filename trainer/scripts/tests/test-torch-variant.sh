#!/usr/bin/env bash
# trainer/scripts/lib/torch-variant.sh 的测试：bash trainer/scripts/tests/test-torch-variant.sh
set -euo pipefail
cd "$(dirname "$0")"
# shellcheck source=SCRIPTDIR/../lib/torch-variant.sh
. ../lib/torch-variant.sh

fails=0
check() { # check <name> <expected> <actual>
	if [ "$2" != "$3" ]; then
		echo "FAIL $1: expected [$2], got [$3]" >&2
		fails=$((fails + 1))
	fi
}
select_is() { # select_is <name> <driver> <ccs> <machine> <expected variant> [expected unsupported]
	tm_select_variant "$2" "$3" "$4"
	check "$1" "$5" "$TM_VARIANT"
	check "$1 (unsupported)" "${6:-}" "$TM_VARIANT_UNSUPPORTED"
	[ -n "$TM_VARIANT_REASON" ] || { echo "FAIL $1: empty reason" >&2; fails=$((fails + 1)); }
}

check "names" "cu130 cu126 cu128 cu118 cpu" "$(tm_variant_names | tr '\n' ' ' | sed 's/ $//')"
check "field torch" "2.14.1" "$(tm_variant_field cu126 torch)"
check "field source" "pypi" "$(tm_variant_field cu130 source)"
check "field archs" "50,60,70,75,80,86,90" "$(tm_variant_field cu126 archs)"
if tm_variant_field nope torch >/dev/null; then check "missing variant" "fail" "ok"; fi

for case in "580 580.65.06 1" "580.65.06 580 0" "550.54.15 525.60.13 0" "525.60.13 525.60.13 0" "525.60.12 525.60.13 1" "1000 580 0" "470.256.02 525.60.13 1"; do
	# shellcheck disable=SC2086
	set -- $case
	if tm_version_ge "$1" "$2"; then got=0; else got=1; fi
	check "version_ge $1 $2" "$3" "$got"
done

check "cc 8.6" 86 "$(tm_cc_to_int 8.6)"
check "cc 12.0" 120 "$(tm_cc_to_int 12.0)"
check "cc 10.3" 103 "$(tm_cc_to_int 10.3)"
if tm_cc_to_int abc >/dev/null 2>&1; then check "cc invalid" fail ok; fi

for case in "89 86,90 0" "90 86,90 0" "100 90 1" "103 100 0" "121 120 0" "61 60 0" "87 80,86 1" "87 87 0" "75 - 1" "37 37,50 0" "35 37 1"; do
	# shellcheck disable=SC2086
	set -- $case
	if tm_cc_covered "$1" "$2"; then got=0; else got=1; fi
	check "covered $1 by $2" "$3" "$got"
done

# 驱动 ≥ 580、Turing 及更新：CUDA 13（PyPI）
select_is "rtx4090 driver 580" 580.82.07 "8.9" x86_64 cu130
select_is "h100+a100 driver 590" 590.44.01 "9.0 8.0" x86_64 cu130
select_is "rtx5090 driver 580" 580.65.06 "12.0" x86_64 cu130
select_is "b300 driver 580" 580.65.06 "10.3" x86_64 cu130
# 驱动 525–579：CUDA 12.6；Blackwell 需要 CUDA 12.8
select_is "rtx3090 driver 550" 550.54.15 "8.6" x86_64 cu126
select_is "rtx4090 driver 570" 570.86.15 "8.9" x86_64 cu126
select_is "rtx5090 driver 575" 575.57.08 "12.0" x86_64 cu128
# 旧显卡：CUDA 13 不支持 Maxwell / Pascal / Volta
select_is "p40 driver 580" 580.65.06 "6.1" x86_64 cu126
select_is "v100 driver 590" 590.44.01 "7.0 7.0" x86_64 cu126
select_is "gtx1080 + rtx3090 driver 560" 560.35.03 "6.1 8.6" x86_64 cu126
# 驱动 450–524：CUDA 11.8
select_is "t4 driver 470" 470.256.02 "7.5" x86_64 cu118
select_is "k80 driver 470" 470.256.02 "3.7 3.7" x86_64 cu118
# 无法同时支持：选支持最多显卡的变体
select_is "p40 + rtx5090 driver 580" 580.65.06 "6.1 12.0 12.0" x86_64 cu130 61
# 退回 CPU
select_is "no gpu" "" "" x86_64 cpu
select_is "gpu without driver" "" "8.6" x86_64 cpu
select_is "driver 440" 440.100 "7.5" x86_64 cpu
select_is "kepler 3.0" 470.256.02 "3.0" x86_64 cpu
# ARM 服务器
select_is "gh200 driver 580" 580.65.06 "9.0" aarch64 cu130
select_is "arm driver 550" 550.54.15 "9.0" aarch64 cpu
select_is "unknown machine" 580.65.06 "9.0" riscv64 cpu

check "cc list" "8.6、12.0" "$(tm_cc_list "86 120")"

if [ "$fails" -gt 0 ]; then
	echo "$fails 项失败" >&2
	exit 1
fi
echo "torch-variant: 全部通过"
