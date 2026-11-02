#!/usr/bin/env bash
# Makefile 中 Docker 部署目标（make up / make down）的测试：bash deploy/scripts/tests/test-makefile.sh
#
# 用记录调用的假 docker compose（COMPOSE=<替身>）运行仓库的 Makefile，不启动任何容器；
# make setup 换成 setup-compute.sh --help（SETUP_ARGS），不检测本机，也不修改 .env。
# 没有 make 时跳过。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

if ! command -v make >/dev/null 2>&1; then
	echo "makefile: 跳过（没有 make）"
	exit 0
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/test-makefile.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

fails=0
checks=0
CASE_NAME=""

fail() {
	echo "FAIL [$CASE_NAME] $*" >&2
	fails=$((fails + 1))
}

# 假的 docker compose：记录参数；config --services 输出 FAKE_SERVICES；
# up 的参数中有 FAKE_FAIL 中的服务时失败（模拟镜像构建失败）。
COMPOSE_FAKE="$WORK/compose"
cat >"$COMPOSE_FAKE" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_LOG"
case "$1" in
config) printf '%s\n' $FAKE_SERVICES ;;
up)
	for a in "$@"; do
		case " ${FAKE_FAIL:-} " in
		*" $a "*)
			echo "failed to solve: process \"/bin/sh -c pip install torch\" did not complete successfully ($a)" >&2
			exit 17
			;;
		esac
	done
	;;
esac
exit 0
EOF
chmod +x "$COMPOSE_FAKE"

# run_make <目标>：在仓库根目录运行 make，合并的输出在 $OUT，退出码在 $RC，假 compose 的调用记录在 $LOG。
# 外层的 make（make test-trainer）通过 MAKEFLAGS 传下来的参数不影响这里。
run_make() {
	LOG="$WORK/$1.log"
	: >"$LOG"
	RC=0
	OUT="$(cd "$ROOT" && env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL -u MAKEOVERRIDES \
		FAKE_LOG="$LOG" FAKE_SERVICES="$FAKE_SERVICES" FAKE_FAIL="$FAKE_FAIL" \
		make --no-print-directory "$1" COMPOSE="$COMPOSE_FAKE" SETUP_ARGS=--help 2>&1)" || RC=$?
	if [ -n "${VERBOSE:-}" ]; then
		printf -- '---- %s：make %s（退出码 %s）\n%s\n' "$CASE_NAME" "$1" "$RC" "$OUT"
	fi
}

expect_rc() {
	checks=$((checks + 1))
	[ "$RC" = "$1" ] || fail "退出码应为 $1，实际为 $RC；输出：
$OUT"
}

expect_rc_nonzero() {
	checks=$((checks + 1))
	[ "$RC" != 0 ] || fail "应失败；输出：
$OUT"
}

expect_calls() { # expect_calls <调用…>：假 compose 的全部调用，按顺序
	checks=$((checks + 1))
	local want
	want="$(printf '%s\n' "$@")"
	[ "$(cat "$LOG")" = "$want" ] || fail "compose 的调用应为：
$want
实际为：
$(cat "$LOG")"
}

expect_out() {
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

# 启用了训练节点：先构建并启动平台，再单独构建并启动训练节点
CASE_NAME="up with trainer"
FAKE_SERVICES="postgres app trainer"
FAKE_FAIL=""
run_make up
expect_rc 0
expect_calls "up -d --build postgres app" "config --services" "up -d --build --no-deps trainer"
expect_out "用法：deploy/scripts/setup-compute.sh"
expect_no_out "警告"

# 训练节点镜像构建失败：平台已经启动，只警告，make up 成功
CASE_NAME="trainer build fails"
FAKE_SERVICES="app postgres trainer-cpu"
FAKE_FAIL="trainer-cpu"
run_make up
expect_rc 0
expect_calls "up -d --build postgres app" "config --services" "up -d --build --no-deps trainer-cpu"
expect_out "警告：训练节点（trainer-cpu）没有构建或启动成功，平台（postgres 与 app）已经启动并可以使用。"
expect_out "docs/deployment.md 第 7 节"

# 平台构建失败：make up 失败，不再启动训练节点
CASE_NAME="app build fails"
FAKE_SERVICES="postgres app trainer"
FAKE_FAIL="app"
run_make up
expect_rc_nonzero
expect_calls "up -d --build postgres app"

# 没有启用训练节点（TM_TRAINER_MODE=off）：只启动平台
CASE_NAME="up without trainer"
FAKE_SERVICES="postgres app"
FAKE_FAIL=""
run_make up
expect_rc 0
expect_calls "up -d --build postgres app" "config --services"

# 其它 profile 中的服务（COMPOSE_PROFILES 中用户自己的 profile）与训练节点一起启动
CASE_NAME="up with other profiles"
FAKE_SERVICES="postgres app trainer monitoring"
run_make up
expect_rc 0
expect_calls "up -d --build postgres app" "config --services" "up -d --build --no-deps trainer monitoring"

# make down 删除所有 profile 中的容器（切换过 COMPOSE_PROFILES 后留下的另一个训练节点也删除）
CASE_NAME="down all profiles"
run_make down
expect_rc 0
expect_calls "--profile * --profile gpu --profile cpu down"

if [ "$fails" -gt 0 ]; then
	echo "makefile: $fails 项失败（共 $checks 项检查）" >&2
	exit 1
fi
echo "makefile: 全部通过（$checks 项检查）"
