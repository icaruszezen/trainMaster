"""trainMaster 训练节点（trainer）：探测本机显卡、CPU 与 CUDA / PyTorch / YOLO 环境，向平台注册并定期上报心跳。

常驻进程只依赖 nvidia-ml-py（NVML）与 psutil；torch / torchvision / ultralytics 只在环境检测子进程中导入，
常驻进程不创建 CUDA 上下文、不占用显存。
"""

__version__ = "0.1.0"
