#!/bin/sh
# trainMaster 独立存储服务容器入口（trainmaster-storage）。
#
# 以 root 启动时：创建数据目录及受管子目录（datasets、exports、.tmp-archives），
# 把目录本身（非递归）的属主修正为 PUID:PGID，然后用 su-exec 降权运行存储服务；
# 以非 root 启动时创建缺失目录后直接运行。不做递归 chown，避免海量小文件启动阻塞。
set -eu

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"
DATA_DIR="${TM_STORAGE_DATA_DIR:-/data}"

warn() { echo "storage-entrypoint: $*" >&2; }

case "$PUID:$PGID" in
	*[!0-9:]* | :* | *:)
		warn "PUID/PGID 必须是数字（当前 PUID=$PUID PGID=$PGID）"
		exit 1
		;;
esac

if [ "$(id -u)" = "0" ]; then
	for dir in "$DATA_DIR" "$DATA_DIR/datasets" "$DATA_DIR/exports" "$DATA_DIR/.tmp-archives"; do
		if ! mkdir -p "$dir"; then
			warn "无法创建目录 $dir"
			continue
		fi
		if [ "$(stat -c '%u:%g' "$dir")" != "$PUID:$PGID" ]; then
			chown "$PUID:$PGID" "$dir" || warn "无法修改 $dir 的属主为 $PUID:$PGID"
		fi
	done
	exec su-exec "$PUID:$PGID" "$@"
fi

for dir in "$DATA_DIR" "$DATA_DIR/datasets" "$DATA_DIR/exports" "$DATA_DIR/.tmp-archives"; do
	mkdir -p "$dir" || warn "无法创建目录 $dir"
done

exec "$@"
