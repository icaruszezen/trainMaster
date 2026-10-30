#!/bin/sh
# trainMaster 容器入口。
#
# 以 root 启动时：创建数据目录，把数据根目录及各子目录本身（非递归）的属主
# 修正为 PUID:PGID，然后用 su-exec 降权运行服务；以非 root 启动时直接运行。
# 不做递归 chown，避免遍历海量图片文件；更换 PUID/PGID 后旧文件的属主需手动修正。
set -eu

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"
DATA_DIR="${TM_DATA_DIR:-/data}"

warn() { echo "entrypoint: $*" >&2; }

case "$PUID:$PGID" in
	*[!0-9:]* | :* | *:)
		warn "PUID/PGID 必须是数字（当前 PUID=$PUID PGID=$PGID）"
		exit 1
		;;
esac

if [ "$(id -u)" = "0" ]; then
	for dir in "$DATA_DIR" "$DATA_DIR/datasets" "$DATA_DIR/models" "$DATA_DIR/runs" \
		"$DATA_DIR/exports" "$DATA_DIR/cache" "$DATA_DIR/tmp"; do
		if ! mkdir -p "$dir"; then
			warn "无法创建目录 $dir"
			continue
		fi
		if [ "$(stat -c '%u:%g' "$dir")" != "$PUID:$PGID" ]; then
			# 部分文件系统（NFS root_squash、某些 Windows 挂载）不允许 chown；
			# 此时继续启动，由服务的可写性检查给出明确错误。
			chown "$PUID:$PGID" "$dir" || warn "无法修改 $dir 的属主为 $PUID:$PGID"
		fi
	done
	exec su-exec "$PUID:$PGID" "$@"
fi

exec "$@"
