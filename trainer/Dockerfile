# trainMaster 训练节点镜像：Python + 按 CUDA 版本选择的 PyTorch 变体 + Ultralytics。
# 构建上下文为 trainer/ 目录：
#
#   docker build --build-arg TORCH_VARIANT=cu130 -t trainmaster-trainer:latest-cu130 trainer
#
# 通常由 docker compose 构建（make setup 按本机驱动与显卡把 TM_TORCH_VARIANT 写入 .env），见 docs/deployment.md。
# CUDA 运行库随 PyTorch 的 pip 包安装，镜像里不需要 CUDA Toolkit；宿主机只需要 NVIDIA 驱动与
# NVIDIA Container Toolkit（容器启动时把驱动库与 /dev/nvidia* 设备挂载进来）。
#
# 国内网络可加速下载：
#   --build-arg DEBIAN_MIRROR=https://mirrors.aliyun.com/debian
#   --build-arg PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple
#   --build-arg TORCH_FIND_LINKS=https://mirrors.aliyun.com/pytorch-wheels
# 内网镜像仓库可替换基础镜像：--build-arg PYTHON_IMAGE=registry.example.com/python:3.12-slim-bookworm。

ARG PYTHON_IMAGE=python:3.12-slim-bookworm
FROM ${PYTHON_IMAGE}

# libgl1、libglib2.0-0：OpenCV（ultralytics 依赖 opencv-python）运行时需要。
# 基础镜像已经包含这些库时（如内网中预先准备好的基础镜像、无法访问 Debian 软件源）跳过 apt。
ARG DEBIAN_MIRROR=
RUN set -eu; \
    if ldconfig -p | grep -q 'libGL\.so\.1 ' && ldconfig -p | grep -q 'libgthread-2\.0\.so\.0 ' \
       && [ -f /etc/ssl/certs/ca-certificates.crt ]; then \
      echo "基础镜像已包含 libGL、GLib 与 CA 证书，跳过 apt-get"; \
    else \
      if [ -n "${DEBIAN_MIRROR}" ]; then \
        for f in /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list; do \
          if [ -f "$f" ]; then sed -i "s|https\{0,1\}://deb.debian.org/debian|${DEBIAN_MIRROR%/}|g" "$f"; fi; \
        done; \
      fi; \
      apt-get update; \
      apt-get install -y --no-install-recommends ca-certificates libgl1 libglib2.0-0; \
      rm -rf /var/lib/apt/lists/*; \
    fi

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_ROOT_USER_ACTION=ignore

WORKDIR /opt/trainmaster-trainer

# ---- 1. PyTorch（单独一层：变体不变时可复用缓存，体积最大）----
ARG PIP_INDEX_URL=https://pypi.org/simple
ARG TORCH_VARIANT=cu130
ARG TORCH_INDEX_URL=
ARG TORCH_FIND_LINKS=
COPY trainmaster_trainer/torch_variants.txt trainmaster_trainer/torch_variants.txt
COPY scripts/lib/torch-variant.sh scripts/lib/torch-variant.sh
COPY scripts/install-torch.sh scripts/install-torch.sh
# 去掉 Windows 检出可能带入的 CRLF。
RUN sed -i 's/\r$//' scripts/install-torch.sh scripts/lib/torch-variant.sh trainmaster_trainer/torch_variants.txt \
 && bash scripts/install-torch.sh "${TORCH_VARIANT}" --python python

# ---- 2. 其余运行依赖（ultralytics、nvidia-ml-py、psutil）----
COPY requirements.txt ./
RUN python -m pip install -r requirements.txt

# ---- 3. 训练节点程序 ----
# 安装到 site-packages 后删除源码与构建产物，镜像中只有一份程序。
COPY pyproject.toml src/pyproject.toml
COPY trainmaster_trainer/ src/trainmaster_trainer/
RUN python -m pip install --no-deps ./src \
 && rm -rf src \
 && python -m pip check

COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN sed -i 's/\r$//' /usr/local/bin/entrypoint.sh && chmod 0755 /usr/local/bin/entrypoint.sh

# NVIDIA_DRIVER_CAPABILITIES：让 NVIDIA Container Toolkit 挂载 CUDA（compute）与 nvidia-smi / NVML（utility）。
# NVIDIA_VISIBLE_DEVICES 不在这里设置，由 compose 的 GPU 设备预留决定使用哪些显卡。
# HOME 指向状态目录：降权后的 PUID 在镜像中没有对应的用户与家目录。
ENV TM_TORCH_VARIANT=${TORCH_VARIANT} \
    NVIDIA_DRIVER_CAPABILITIES=compute,utility \
    YOLO_OFFLINE=1 \
    TM_STATE_DIR=/var/lib/trainmaster \
    HOME=/var/lib/trainmaster

# 冒烟检查（构建时没有 GPU）：依赖能导入，CPU 上的 YOLO 前向推理正常；状态写到临时目录，不留在镜像里。
RUN HOME=/tmp/trainmaster-smoke TM_STATE_DIR=/tmp/trainmaster-smoke TM_DATA_DIR=/tmp/trainmaster-smoke \
    python -m trainmaster_trainer selftest \
 && rm -rf /tmp/trainmaster-smoke

# 运行时的工作目录为状态目录（也是 HOME）。
WORKDIR /var/lib/trainmaster

# 启动时注册与第一次 CUDA 环境检测需要一些时间，留出 start-period。
HEALTHCHECK --interval=30s --timeout=10s --start-period=120s --retries=3 \
  CMD python -m trainmaster_trainer healthcheck

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["python", "-m", "trainmaster_trainer", "run"]
