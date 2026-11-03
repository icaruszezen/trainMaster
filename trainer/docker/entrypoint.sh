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
DEV_DIR="${TM_DEV_DIR:-/dev}" # 测试用：模拟 /dev

warn() { echo "entrypoint: $*" >&2; }

# 降权后的附加组。/dev/nvidia* 默认所有用户可读写；驱动设置了 NVreg_DeviceFileMode=0660 与
# NVreg_DeviceFileGID（如 video 组）时，容器中保留宿主机设备文件的权限与属组，需要加入该组才能使用显卡。
nvidia_groups() {
	groups=""
	# 以 root 运行（PUID=0）时可以访问所有设备，不需要附加组，也不需要提示
	[ "$PUID" != 0 ] || return 0
	for dev in "$DEV_DIR"/nvidiactl "$DEV_DIR"/nvidia-uvm "$DEV_DIR"/nvidia[0-9]*; do
		[ -e "$dev" ] || continue
		mode="$(stat -c %A "$dev" 2>/dev/null)" || continue
		case "$mode" in
			*rw?) continue ;; # 其他用户可读写
		esac
		gid="$(stat -c %g "$dev" 2>/dev/null)" || continue
		if [ "$gid" = "$PGID" ]; then
			continue
		fi
		if [ "$gid" = 0 ]; then
			warn "$dev 只允许 root 访问（$mode），以 PUID=$PUID 运行时无法使用显卡；可设置 PUID=0 PGID=0，或修改宿主机驱动的设备权限"
			continue
		fi
		case ",$groups," in
			*",$gid,"*) ;;
			*) groups="${groups:+$groups,}$gid" ;;
		esac
	done
	printf '%s' "$groups"
}

# 只给出子命令时补全为 python -m trainmaster_trainer <子命令>，
# 例如 docker compose run --rm trainer probe、docker run … selftest --json、… export --weights x.pt。
case "${1:-}" in
	"") set -- python -m trainmaster_trainer run ;;
	run | probe | selftest | export | healthcheck | -*) set -- python -m trainmaster_trainer "$@" ;;
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
	# 镜像中没有 PUID 对应的用户：不继承 root 的附加组，只加入 /dev/nvidia* 的属组（需要时）。
	extra="$(nvidia_groups)" || extra=""
	if [ -n "$extra" ]; then
		exec setpriv --reuid="$PUID" --regid="$PGID" --groups="$extra" -- "$@"
	fi
	exec setpriv --reuid="$PUID" --regid="$PGID" --clear-groups -- "$@"
fi

exec "$@"
