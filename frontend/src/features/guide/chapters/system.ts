import recycleBinImg from "@/features/guide/assets/recycle-bin.webp"
import storagePageImg from "@/features/guide/assets/storage-page.webp"
import type { GuideChapter } from "@/features/guide/types"

export const systemChapters: GuideChapter[] = [
  {
    id: "storage",
    groupId: "system",
    title: "存储管理与数据迁移",
    shortTitle: "存储管理",
    summary:
      "配置并管理本机目录、独立存储容器、S3 兼容对象存储（MinIO / RustFS / 云 S3）或 SMB 共享文件夹，支持不停机切换当前写入节点、后台校验迁移历史数据以及配置训练机缓存策略。",
    moduleId: "storage",
    targetPath: "/system/storage",
    targetLabel: "存储管理",
    keywords: [
      "存储管理",
      "存储节点",
      "MinIO",
      "S3",
      "SMB",
      "NAS",
      "迁移",
      "数据迁移",
      "训练机缓存",
      "LRU",
      "磁盘空间",
    ],
    sections: [
      {
        id: "storage-nodes-migration",
        title: "管理存储节点、后台迁移与训练机缓存策略",
        summary:
          "平台采用「全局单当前写入节点」设计：切换激活节点后，新建数据集与数据版本自动写入新节点，而留在旧节点的历史数据依然可以正常浏览、标注与训练，随时可按需一键后台迁移。",
        figures: [
          {
            id: "fig-storage-page",
            src: storagePageImg,
            alt: "存储管理页面截图，展示本机默认存储、工厂 MinIO 节点、迁移面板与训练机缓存策略",
            caption:
              "存储管理页：新增存储节点、当前激活节点、历史数据迁移与训练机缓存策略",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "刷新与「新增存储节点」",
                description:
                  "点击「新增存储节点」配置远程 S3 对象存储（MinIO / Ceph / 云 S3）、SMB 共享目录（Windows / NAS）、Docker 存储容器或自定义挂载目录，配置前支持一键测试连通性；凭证均以 AES-256-GCM 加密存库。",
                x: 83.4,
                y: 10.9,
                box: { x: 84.0, y: 8.8, width: 14.4, height: 4.2 },
              },
              {
                index: 2,
                label: "当前激活存储节点（绿框标识）",
                description:
                  "标有「当前使用中」的节点会承接所有新建数据集与新生成数据版本的写入；卡片展示该节点的数据集数、数据版本数、磁盘使用进度条与连通状态。",
                x: 20.4,
                y: 24.8,
                box: { x: 19.4, y: 23.7, width: 25.6, height: 39.8 },
              },
              {
                index: 3,
                label: "备用/远程存储节点操作",
                description:
                  "点击「测试连接」验证网络与权限，点击「设为当前存储」立即切换全局写入目标（无需重启服务），右侧图标可编辑配置或删除无数据驻留的节点。",
                x: 47.0,
                y: 24.8,
                box: { x: 46.1, y: 23.7, width: 25.6, height: 39.8 },
              },
              {
                index: 4,
                label: "历史数据迁移与空间释放",
                description:
                  "切换激活节点后，仍留在旧节点上的数据集与已就绪数据版本会自动列在此处；支持勾选一键后台迁移，完整性校验通过后自动释放旧节点空间。",
                x: 20.4,
                y: 67.4,
                box: { x: 19.4, y: 66.2, width: 46.5, height: 24.5 },
              },
              {
                index: 5,
                label: "训练机短期数据集缓存策略",
                description:
                  "默认关闭（训练结束立即清理训练机上的临时数据副本，保持零磁盘残留）；若多轮调参频繁复用同一远程数据版本，可开启 LRU 缓存并设置最多保留版本数与最长保留小时数。",
                x: 68.0,
                y: 67.4,
                box: { x: 67.0, y: 66.2, width: 31.3, height: 33.0 },
              },
            ],
          },
        ],
        steps: [
          {
            title: "扩容切换到独立的 S3 / NAS 存储服务器",
            description:
              "点击右上角「新增存储节点」，填写存储类型、地址、桶名/共享路径与访问凭证，先点「测试连接」确认返回绿色正常再保存；随后在卡片上点击「设为当前存储」。",
          },
          {
            title: "按需迁移旧数据集释放平台本机硬盘",
            description:
              "在「历史数据迁移与空间释放」区域勾选需要搬迁的数据集或数据版本并启动迁移，页面实时显示文件与字节进度；页面底部还可查看平台本机系统目录（models / runs / reviews / cache / tmp）的容量与读写状态。",
          },
        ],
        tips: [
          {
            variant: "note",
            title: "哪些文件始终保存在平台本机？",
            content:
              "切换远程存储节点仅迁移体量最大的「数据集（datasets）」与「数据版本（exports）」；模型权重（models）、训练/评估产物（runs）、审图临时标注（reviews）和缩略图缓存（cache）始终保留在平台本机目录以保证快速响应。",
          },
        ],
      },
    ],
  },
  {
    id: "recycle-bin",
    groupId: "system",
    title: "回收站与 7 天恢复",
    shortTitle: "回收站",
    summary:
      "平台为所有重要视觉资产提供 7 天强制冷静期：删除的数据集、审图版本、数据版本与模型（含随项目一并删除的版本和模型）均可随时一键恢复，到期后自动永久清理。",
    moduleId: "recycle-bin",
    targetPath: "/system/recycle-bin",
    targetLabel: "回收站",
    keywords: [
      "回收站",
      "恢复",
      "误删",
      "7 天",
      "保留期",
      "换名恢复",
      "永久删除",
    ],
    sections: [
      {
        id: "recycle-bin-restore",
        title: "查看保留倒计时与一键恢复误删资产",
        figures: [
          {
            id: "fig-recycle-bin",
            src: recycleBinImg,
            alt: "回收站页面截图，展示 7 天保留声明、分类统计与可恢复项目列表",
            caption: "回收站：7 天强制保留说明、分类统计、到期倒计时与一键恢复",
            width: 1600,
            height: 1000,
            callouts: [
              {
                index: 1,
                label: "7 天强制保留安全机制",
                description:
                  "平台刻意不提供「提前永久删除」或「清空回收站」按钮，防止误操作导致珍贵产线数据瞬间丢失；所有回收项均保留完整 7 天后由后台自动清理。",
                x: 20.2,
                y: 21.6,
                box: { x: 19.4, y: 20.6, width: 78.9, height: 7.4 },
              },
              {
                index: 2,
                label: "分类计数与最早到期提醒",
                description:
                  "按「数据集 / 审图版本 / 数据版本 / 模型」分类统计回收项数量与磁盘占用，最右侧提示最快即将到期永久清理的具体条目。",
                x: 84.2,
                y: 31.2,
                box: { x: 19.4, y: 30.0, width: 78.9, height: 15.6 },
              },
              {
                index: 3,
                label: "搜索与按资源类型筛选",
                description:
                  "输入名称或说明关键词，或通过下拉菜单快速只看某一种被删资源。",
                x: 51.2,
                y: 50.0,
                box: { x: 19.4, y: 47.8, width: 31.0, height: 4.4 },
              },
              {
                index: 4,
                label: "剩余保留期与「恢复」按钮",
                description:
                  "进度条直观显示距离永久删除的剩余天数；点击「恢复」立即还原——数据集会回到原项目与原审图任务，随项目一起删除的数据版本或模型会自动连同原项目一并恢复。",
                x: 91.5,
                y: 62.6,
                box: { x: 81.5, y: 59.6, width: 16.0, height: 12.6 },
              },
            ],
          },
        ],
        tips: [
          {
            variant: "tip",
            title: "重名时自动引导「换名恢复」",
            content:
              "如果删除某数据集或项目后，又新建了同名项，点击「恢复」时系统会弹出换名输入框，只需输入新名称即可无损找回全部图片与标注；另外，刚执行删除时页面顶部弹出的通知条里也可直接点「撤销」。",
          },
          {
            variant: "warning",
            title: "哪些内容不会进入回收站？",
            content:
              "训练任务、模型评估记录以及尚未生成完毕/生成失败的数据版本不含需要保护的成品资产，删除时直接清除，不进入回收站。",
          },
        ],
      },
    ],
  },
]
