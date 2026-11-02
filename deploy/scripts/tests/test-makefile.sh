#!/usr/bin/env bash
# Makefile 中 Docker 部署目标（make up / make down）的测试：bash deploy/scripts/tests/test-makefile.sh
#
# 用记录调用的假 docker compose（COMPOSE=<替身>）运行仓库的 Makefile，不启动任何容器；
# make setup 换成 setup-compute.sh --help（SETUP_ARGS），不检测本机，也不修改 .env。
# 构建时间（TM_BUILD_TIME）的部分用例在临时目录中的 Makefile 副本上运行（临时的 git 仓库）。
# 没有 make 时跳过；没有 git 时跳过构建时间的用例。
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

# 假的 docker compose：记录参数，以及 make 导出的 TM_BUILD_TIME（app 镜像的构建参数 BUILD_TIME）；
# config --services 输出 FAKE_SERVICES；up 的参数中有 FAKE_FAIL 中的服务时失败（模拟镜像构建失败）。
COMPOSE_FAKE="$WORK/compose"
cat >"$COMPOSE_FAKE" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_LOG"
printf '%s\n' "${TM_BUILD_TIME-（未设置）}" >>"$FAKE_LOG.build-time"
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

# run_make <目标>：在 $MAKE_DIR（默认仓库根目录）运行 make，合并的输出在 $OUT，退出码在 $RC，
# 假 compose 的调用记录在 $LOG，它看到的 TM_BUILD_TIME 在 $LOG.build-time；MAKE_ENV 是额外的环境变量。
# 外层的 make（make test-trainer）通过 MAKEFLAGS 传下来的参数与导出的版本信息不影响这里。
MAKE_DIR="$ROOT"
MAKE_ENV=()
run_make() {
	LOG="$WORK/$1.log"
	: >"$LOG"
	: >"$LOG.build-time"
	RC=0
	OUT="$(cd "$MAKE_DIR" && env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL -u MAKEOVERRIDES \
		-u TM_VERSION -u TM_COMMIT -u TM_BUILD_TIME \
		FAKE_LOG="$LOG" FAKE_SERVICES="$FAKE_SERVICES" FAKE_FAIL="$FAKE_FAIL" ${MAKE_ENV[@]+"${MAKE_ENV[@]}"} \
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

expect_build_time() { # expect_build_time <值>：假 compose 每次调用时看到的 TM_BUILD_TIME 都是这个值
	checks=$((checks + 1))
	local got
	got="$(sort -u "$LOG.build-time")"
	[ "$got" = "$1" ] || fail "TM_BUILD_TIME 应为 [$1]，实际为 [$got]"
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

# 构建时间：代码没有变化时每次 make up 都相同（app 镜像的构建参数不变，compose 复用构建缓存，
# 不重启正在运行的 app，正在进行的数据集导入不会被中断）
if command -v git >/dev/null 2>&1; then
	# 仓库本身是 git 检出时（源码包中没有 .git，构建时间为当前时间）
	if (cd "$ROOT" && git rev-parse --verify -q HEAD >/dev/null 2>&1); then
		CASE_NAME="build time stable"
		FAKE_SERVICES="postgres app trainer"
		FAKE_FAIL=""
		run_make up
		expect_rc 0
		first="$(sort -u "$LOG.build-time")"
		sleep 1
		run_make up
		expect_rc 0
		second="$(sort -u "$LOG.build-time")"
		checks=$((checks + 1))
		[ -n "$first" ] && [ "$first" = "$second" ] || fail "两次 make up 的 TM_BUILD_TIME 应相同，实际为 [$first] 与 [$second]"
	fi

	# 默认为当前提交（HEAD）的提交时间，换算为 UTC；.env 与环境变量中的值优先
	CASE_NAME="build time from commit"
	GIT_REPO="$WORK/git-repo"
	mkdir -p "$GIT_REPO"
	cp "$ROOT/Makefile" "$GIT_REPO/"
	if ! (
		unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
		cd "$GIT_REPO" &&
			git -c init.defaultBranch=main init -q &&
			git add Makefile &&
			GIT_AUTHOR_DATE="2026-01-01T00:00:00+00:00" GIT_COMMITTER_DATE="2026-03-04T05:06:07+08:00" \
				git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit -q -m test
	) >"$WORK/git.log" 2>&1; then
		fail "无法创建临时 git 仓库：$(cat "$WORK/git.log")"
	fi
	MAKE_DIR="$GIT_REPO"
	MAKE_ENV=(TZ=Asia/Shanghai GIT_CEILING_DIRECTORIES="$WORK")
	run_make docker-build
	expect_rc 0
	expect_calls "build app"
	expect_build_time "2026-03-03T21:06:07Z"
	printf 'TM_BUILD_TIME=2020-05-06T07:08:09Z\n' >"$GIT_REPO/.env"
	run_make docker-build
	expect_build_time "2020-05-06T07:08:09Z"
	MAKE_ENV+=(TM_BUILD_TIME=2021-02-03T04:05:06Z)
	run_make docker-build
	expect_build_time "2021-02-03T04:05:06Z"

	# 不是 git 检出（如下载的源码包）：当前时间
	CASE_NAME="build time without git checkout"
	MAKE_DIR="$WORK/no-git"
	mkdir -p "$MAKE_DIR"
	cp "$ROOT/Makefile" "$MAKE_DIR/"
	MAKE_ENV=(GIT_CEILING_DIRECTORIES="$WORK")
	before="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	run_make docker-build
	after="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	expect_rc 0
	got="$(sort -u "$LOG.build-time")"
	checks=$((checks + 1))
	if [[ ! "$got" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || [[ "$got" < "$before" ]] || [[ "$got" > "$after" ]]; then
		fail "TM_BUILD_TIME 应为当前时间（$before – $after），实际为 [$got]"
	fi
	MAKE_DIR="$ROOT"
	MAKE_ENV=()
else
	echo "makefile: 跳过构建时间的用例（没有 git）"
fi

if [ "$fails" -gt 0 ]; then
	echo "makefile: $fails 项失败（共 $checks 项检查）" >&2
	exit 1
fi
echo "makefile: 全部通过（$checks 项检查）"
