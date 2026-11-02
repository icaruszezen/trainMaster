#!/bin/sh
# trainMaster 训练节点容器入口。
#
# 以 root 启动时：创建状态目录（节点标识 node-key、心跳文件、Ultralytics 配置），把状态目录本身
# （非递归）的属主修正为 PUID:PGID，然后用 setpriv（util-linux）降权运行；以非 root 启动时直接运行。
# 不做递归 chown：更换 PUID/PGID 后旧文件的属主需手动修正（与 app 容器的入口相同）。
# 存储目录 /data 由 app 容器负责创建并修正属主，训练节点只读写其中的文件。
set -eu

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"
STATE_DIR="${TM_STATE_DIR:-/var/lib/trainmaster}"

warn() { echo "entrypoint: $*" >&2; }

# 只给出子命令时补全为 python -m trainmaster_trainer <子命令>，
# 例如 docker compose run --rm trainer probe、docker run … selftest --json。
case "${1:-}" in
	"") set -- python -m trainmaster_trainer run ;;
	run | probe | selftest | healthcheck | -*) set -- python -m trainmaster_trainer "$@" ;;
esac

for id in "$PUID" "$PGID"; do
	case "$id" in
		"" | *[!0-9]*)
			warn "PUID/PGID 必须是数字（当前 PUID=$PUID PGID=$PGID）"
			exit 1
			;;
	esac
done

if [ "$(id -u)" = "0" ]; then
	if mkdir -p "$STATE_DIR"; then
		if [ "$(stat -c '%u:%g' "$STATE_DIR")" != "$PUID:$PGID" ]; then
			# 部分文件系统（NFS root_squash、某些 Windows 挂载）不允许 chown；此时继续启动，
			# 训练节点会退回到可写的目录并在日志中说明（节点标识不能持久保存）。
			chown "$PUID:$PGID" "$STATE_DIR" || warn "无法修改 $STATE_DIR 的属主为 $PUID:$PGID"
		fi
	else
		warn "无法创建目录 $STATE_DIR"
	fi
	# --clear-groups：镜像中没有 PUID 对应的用户，不继承 root 的附加组。/dev/nvidia* 设备对所有用户可读写。
	exec setpriv --reuid="$PUID" --regid="$PGID" --clear-groups -- "$@"
fi

exec "$@"
