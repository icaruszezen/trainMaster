#!/usr/bin/env bash
# trainer/docker/entrypoint.sh 的测试：bash trainer/scripts/tests/test-entrypoint.sh
# 用假的 python 记录收到的参数；PUID/PGID 设为当前用户，以 root 运行时 setpriv 也能成功。
set -euo pipefail
cd "$(dirname "$0")"
entrypoint="$(cd ../../docker && pwd)/entrypoint.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
cat >"$tmp/bin/python" <<'EOF'
#!/bin/sh
echo "python $* uid=$(id -u)"
EOF
chmod +x "$tmp/bin/python"

fails=0
run_entry() { # run_entry <参数…>：输出存入 out，退出码存入 code
	set +e
	out="$(env PATH="$tmp/bin:$PATH" PUID="${PUID:-$(id -u)}" PGID="${PGID:-$(id -g)}" \
		TM_STATE_DIR="$tmp/state" TM_DEV_DIR="${TM_DEV_DIR:-$tmp/no-dev}" sh "$entrypoint" "$@" 2>&1)"
	code=$?
	set -e
}
check() { # check <name> <expected> <actual>
	if [ "$2" != "$3" ]; then
		echo "FAIL $1: expected [$2], got [$3]" >&2
		fails=$((fails + 1))
	fi
}

uid="$(id -u)"
run_entry
check "default command" "python -m trainmaster_trainer run uid=$uid" "$out"
run_entry probe
check "probe" "python -m trainmaster_trainer probe uid=$uid" "$out"
run_entry selftest --json
check "selftest --json" "python -m trainmaster_trainer selftest --json uid=$uid" "$out"
run_entry export --weights /tmp/best.pt --imgsz 640
check "export" "python -m trainmaster_trainer export --weights /tmp/best.pt --imgsz 640 uid=$uid" "$out"
run_entry healthcheck
check "healthcheck" "python -m trainmaster_trainer healthcheck uid=$uid" "$out"
run_entry --version
check "option only" "python -m trainmaster_trainer --version uid=$uid" "$out"
run_entry python -m trainmaster_trainer run
check "full command" "python -m trainmaster_trainer run uid=$uid" "$out"
run_entry echo hello
check "other command" "hello" "$out"

PUID=abc run_entry probe
check "invalid PUID exit code" 1 "$code"
case "$out" in
*"PUID/PGID 必须是数字"*) ;;
*) check "invalid PUID message" "PUID/PGID 必须是数字" "$out" ;;
esac
PGID="1:2" run_entry probe
check "PGID with colon exit code" 1 "$code"
check "PGID with colon message" "entrypoint: PUID/PGID 必须是数字（当前 PUID=$uid PGID=1:2）" "$out"

if [ "$uid" = 0 ]; then
	# 以 root 运行：创建状态目录、修正属主（非递归）并降权
	chmod 755 "$tmp" # 降权后的用户需要能执行假 python
	mkdir -p "$tmp/state/sub"
	PUID=1234 PGID=2345 run_entry probe
	check "drop privileges" "python -m trainmaster_trainer probe uid=1234" "$out"
	check "state dir owner" "1234:2345" "$(stat -c %u:%g "$tmp/state")"
	check "not recursive" "0:0" "$(stat -c %u:%g "$tmp/state/sub")"

	# 不继承 root 的附加组
	PUID=1234 PGID=2345 run_entry id -G
	check "no supplementary groups" "2345" "$out"

	# /dev/nvidia* 对所有用户开放（默认）：不需要附加组
	mkdir -p "$tmp/dev"
	for dev in nvidiactl nvidia-uvm nvidia0; do
		: >"$tmp/dev/$dev"
		chmod 666 "$tmp/dev/$dev"
	done
	TM_DEV_DIR="$tmp/dev" PUID=1234 PGID=2345 run_entry id -G
	check "world-accessible devices" "2345" "$out"

	# 设备只对属组开放（NVreg_DeviceFileMode=0660、NVreg_DeviceFileGID=video）：加入该组
	chgrp 3456 "$tmp/dev/nvidiactl" "$tmp/dev/nvidia0"
	chmod 660 "$tmp/dev/nvidiactl" "$tmp/dev/nvidia0"
	: >"$tmp/dev/nvidia1"
	chgrp 2345 "$tmp/dev/nvidia1"
	chmod 660 "$tmp/dev/nvidia1"
	TM_DEV_DIR="$tmp/dev" PUID=1234 PGID=2345 run_entry id -G
	check "device group added" "2345 3456" "$out"

	# 只允许 root 访问：不加入 root 组，给出提示
	chgrp 0 "$tmp/dev/nvidiactl" "$tmp/dev/nvidia0"
	TM_DEV_DIR="$tmp/dev" PUID=1234 PGID=2345 run_entry id -G
	check "root-only device exit code" 0 "$code"
	case "$out" in
	*"只允许 root 访问"*) ;;
	*) check "root-only device warning" "只允许 root 访问" "$out" ;;
	esac
	check "root group not added" "2345" "$(printf '%s\n' "$out" | tail -n 1)"

	# 以 root 运行（PUID=0）：能访问所有设备，不提示
	TM_DEV_DIR="$tmp/dev" PUID=0 PGID=2345 run_entry id -G
	check "root puid exit code" 0 "$code"
	check "root puid no warning" "2345" "$out"
fi

if [ "$fails" -gt 0 ]; then
	echo "$fails test(s) failed" >&2
	exit 1
fi
echo "entrypoint.sh: all tests passed"
