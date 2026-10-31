# 架构说明

本文描述 trainMaster 的整体结构与约定（平台框架 + 已实现的数据集管理）。后续功能开发应遵循这里的约定；需要调整约定时，请同步更新本文。

## 1. 组件总览

```mermaid
flowchart LR
    browser["浏览器<br/>React SPA"]

    subgraph host["服务器（Docker Compose，项目名 trainmaster）"]
        subgraph app["app 容器"]
            server["trainmaster（Go）<br/>/api/* JSON API<br/>其余路径：前端静态文件"]
        end
        pg[("postgres 容器<br/>PostgreSQL 17")]
        trainer["trainer 容器（规划中）<br/>GPU 训练 worker"]
    end

    storage[/"宿主机目录 ./data/storage<br/>（容器内 /data）"/]
    pgdata[/"宿主机目录 ./data/postgres"/]

    browser -- "HTTP :8080" --> server
    server -- "SQL（元数据）" --> pg
    server -- "读写文件" --> storage
    pg --- pgdata
    trainer -. "领取任务 / 上报进度（HTTP）" .-> server
    trainer -. "读数据集、写模型与运行产物" .-> storage
```

- **单镜像**：Go 服务同时提供 `/api` 下的 JSON API 和前端构建产物（`/app/web`），部署只需 `app` 与 `postgres` 两个容器。
- **文件与元数据分离**：图片、模型权重等大文件以普通文件形式保存在宿主机目录；PostgreSQL 只保存元数据（记录、关系、状态）。两者都通过绑定挂载持久化。
- **内网友好**：前端不依赖任何外部 CDN、在线字体或第三方服务。

## 2. 后端模块

后端位于 `backend/`，Go module 为 `github.com/icaruszezen/trainmaster/backend`。依赖保持最少：chi（路由）、pgx（PostgreSQL），日志使用标准库 `log/slog`，不使用 ORM、配置库或迁移库。数据集导入额外使用纯 Go 的解压与图像库（`bodgit/sevenzip`、`nwaples/rardecode`、`ulikunitz/xz`、`xi2/xz`、`klauspost/compress`、`golang.org/x/image`、`golang.org/x/text`），不依赖系统命令，各平台行为一致。

| 包 | 职责 |
|---|---|
| `cmd/trainmaster` | 程序入口，按顺序组装各模块并处理优雅关闭 |
| `internal/buildinfo` | 版本号、提交号、构建时间（通过 `-ldflags -X` 注入） |
| `internal/config` | 读取并校验 `TM_*` 环境变量；非法值一次性全部报出并退出 |
| `internal/database` | pgxpool 连接池、启动时连接重试、内置迁移执行器 |
| `internal/storage` | 数据目录布局、启动时创建与可写检查、磁盘用量、安全路径解析 |
| `internal/dataset` | 数据集：命名规则、图片与 VOC 标注识别、压缩包解压、目录扫描、缩略图、导入任务执行器 |
| `internal/project` | 项目：项目的增删改、项目与数据集的多对多关系、按数据集合计的统计与标签分布 |
| `internal/httpapi` | chi 路由、中间件、统一 JSON 响应与错误信封、各 handler |
| `internal/web` | 前端静态文件服务：SPA 回退、缓存头、禁止目录列表与路径穿越 |

### 启动流程

1. 加载配置，初始化 slog（`TM_LOG_FORMAT=text|json`）。
2. 初始化存储目录：创建根目录和 6 个子目录（见第 5 节）并做可写探测，失败则退出并提示检查挂载目录权限 / PUID、PGID。
3. 连接数据库：指数退避重试，最长约 60 秒（数据库容器还在启动时会等待），仍失败则以非 0 退出。
4. `TM_DB_AUTO_MIGRATE=true`（默认）时执行待执行的迁移。
5. 启动数据集导入任务执行器，并恢复上次关闭时未完成的任务（见第 6 节）。
6. 启动 HTTP 服务；收到 SIGINT / SIGTERM 后停止接收新请求，等待进行中的请求完成（最长 15 秒）；超时后强制关闭剩余连接（取消其请求的 context），最后关闭数据库连接池。还在等待数据库或迁移锁时收到信号，则直接正常退出。收到信号时会立即取消正在执行的导入 / 扫描任务并记为失败（“服务重启，导入被中断”），排队中的任务保留到下次启动执行；HTTP 服务关闭后再等待任务执行器的 worker 退出。进程在写入结果前异常退出时，下次启动由恢复逻辑把任务标记为中断，并重新计算受影响数据集的统计数据。以后的长连接 handler（如 SSE 推送训练日志）应通过 `http.Server.RegisterOnShutdown` 在关闭开始时主动结束。

## 3. API 约定

### 基本规则

- 所有接口都在 `/api` 下，业务接口带版本前缀 `/api/v1`。
- 请求与响应均为 JSON，响应头 `Content-Type: application/json; charset=utf-8`，并带 `Cache-Control: no-store`。
- 成功时**直接返回资源对象**；列表统一为：

  ```json
  {"items": [], "total": 0}
  ```

  （Go 中使用 `httpapi.NewList(items, total)`，空列表编码为 `[]` 而不是 `null`。）
- 失败时统一返回错误信封，`code` 供程序判断，`message` 直接展示给用户（中文）：

  ```json
  {"error": {"code": "not_found", "message": "接口不存在"}}
  ```

- 每个请求都有 `X-Request-ID`：请求头中带了合法值（≤128 个可见 ASCII 字符）就沿用，否则自动生成；响应头会返回它，访问日志中也会记录，便于排查问题。
- `HEAD` 请求自动由对应的 `GET` 路由处理。

### 错误码

| HTTP 状态 | code | 场景 |
|---|---|---|
| 400 | `bad_request` | 请求格式不正确（如 JSON 无法解析、参数类型错误） |
| 404 | `not_found` | 接口或资源不存在（未设置 `TM_WEB_DIR` 时，非 `/api` 路径也返回它） |
| 405 | `method_not_allowed` | 路径存在但不支持该方法（响应头 `Allow` 列出支持的方法） |
| 409 | `conflict` | 与当前状态冲突（同名数据集、数据集正在导入、任务状态不允许该操作等） |
| 409 | `upload_offset_mismatch` | 分块上传的 `offset` 与服务端已接收的字节数不一致，客户端应重新获取任务后从 `received_bytes` 续传 |
| 413 | `payload_too_large` | 请求体超过限制（单个分块 64 MiB，单次文件夹批量上传 256 MiB） |
| 422 | `validation_failed` | 字段校验失败，`message` 说明具体问题（如数据集名称不合法） |
| 422 | `unsupported_image` | 无法为该图片生成缩略图（格式不支持、文件损坏或像素过大），前端回退为原图或占位图 |
| 500 | `internal_error` | 未预期的错误（handler panic 会被恢复并返回此错误） |
| 501 | `not_implemented` | 规划中的功能模块 |
| 507 | `insufficient_storage` | 存储空间不足，无法接收该上传 |

新增功能需要其它错误码时，在 `internal/httpapi/respond.go` 中集中定义，并补充到本表。

### 已有接口

| 方法与路径 | 说明 |
|---|---|
| `GET /api/healthz` | 存活探针，不依赖任何外部组件，返回 `{"status":"ok"}`。Docker HEALTHCHECK 使用它 |
| `GET /api/readyz` | 就绪探针：检查数据库与存储，全部正常 200，否则 503，失败项带 `error` |
| `GET /api/v1/system/info` | 版本、运行时间、数据库与存储信息（工作台和存储管理页使用）。数据库不可用时仍返回 200，`database.status` 为 `unavailable` |
| 数据集相关接口 | 见下方“数据集接口” |
| 项目相关接口 | 见下方“项目接口” |
| `/api/v1/annotation/tasks`、`/api/v1/annotation/labels`、`/api/v1/training/jobs`、`/api/v1/evaluations`、`/api/v1/models` 及其子路径 | 预留给规划中的模块，任意方法均返回 `501 not_implemented` |

`/api/readyz` 示例：

```json
{
  "status": "ok",
  "checks": {
    "database": {"status": "ok", "latency_ms": 0.53},
    "storage": {"status": "ok"}
  }
}
```

`/api/v1/system/info` 示例（字段名是与前端 `frontend/src/features/system/api.ts` 的契约，不要随意改名）：

```json
{
  "name": "trainMaster",
  "version": "0.1.0-dev",
  "commit": "d62132f",
  "build_time": "2026-10-30T08:00:00Z",
  "go_version": "go1.24.13",
  "started_at": "2026-10-30T08:00:00Z",
  "uptime_seconds": 120,
  "database": {"status": "ok", "server_version": "17.6", "schema_version": 1},
  "storage": {
    "root": "/data",
    "total_bytes": 1000, "free_bytes": 400, "used_bytes": 600,
    "directories": [{"key": "datasets", "path": "/data/datasets", "writable": true}]
  }
}
```

`used_bytes = total_bytes - free_bytes`，`free_bytes` 为非特权进程可用空间。磁盘用量只在 Linux 上实现（部署环境），其它平台上这三个值为 0。

### 数据集接口

字段定义以前端 `frontend/src/features/datasets/api.ts` 中的类型为准（与后端 `internal/httpapi/datasets.go` 保持一致）。`{id}` 等路径参数必须是正整数，否则返回 404。

| 方法与路径 | 说明 |
|---|---|
| `GET /api/v1/datasets` | 数据集列表（`q` 按名称 / 描述筛选；`project_id=<项目ID>` 只列出该项目中的数据集，`project_id=none` 只列出未纳入任何项目的数据集，其它值返回 400），按更新时间倒序；每项带统计数据、封面图片（最多 4 张）、所属项目 `projects: [{id, name}]`（按名称排序）和进行中的导入任务 |
| `POST /api/v1/datasets` | `{name, description?}` 新建数据集并创建同名文件夹；带 `directory` 时把 `datasets/` 下已有的文件夹添加为数据集，并立即开始扫描 |
| `GET /api/v1/datasets/directories` | `datasets/` 下的全部一级文件夹（跳过隐藏目录和符号链接），标明是否已添加，附图片 / 标注文件数量（超过 5 万个条目或 2 秒时为估算值，`approximate: true`） |
| `GET /api/v1/datasets/{id}` | 数据集详情；`directory_missing` 表示文件夹已不在磁盘上 |
| `PATCH /api/v1/datasets/{id}` | 修改名称（只改显示名称，文件夹不变）和描述 |
| `PUT /api/v1/datasets/{id}/projects` | `{project_ids: [...]}` 设置数据集所属的项目（整体替换，`[]` 表示移出所有项目，缺少该字段时返回 422），返回更新后的数据集；所选项目不存在时返回 422，不做任何修改 |
| `DELETE /api/v1/datasets/{id}` | 删除数据集记录（同时移出所属的项目）；`?delete_files=true` 同时删除磁盘上的**整个**文件夹与缩略图缓存。成功返回 204；记录已删除但文件夹没能完全删除（如权限不足）时返回 200 `{"files_removed": false, "message": "…"}`，剩余文件恢复为原来的文件夹名，可以重新添加或手动清理。导入 / 扫描正在执行时返回 409 |
| `GET /api/v1/datasets/{id}/images` | 图片列表：`offset`、`limit`（≤ 200）、`status`（`all` / `annotated` 有目标 / `empty` 空标注 / `invalid` 标注无效 / `unannotated` 无标注文件）、`label`、`q`（路径包含）、`sort`（`path` / `-mod_time` / `-objects`） |
| `GET /api/v1/datasets/{id}/images/{imageId}` | 单张图片详情：`objects` 等标注信息实时从 VOC 文件解析；文件无法解析或已不存在时 `annotation_error` 给出原因 |
| `GET /api/v1/datasets/{id}/images/{imageId}/file` | 原图（支持 Range 与条件请求） |
| `GET /api/v1/datasets/{id}/images/{imageId}/thumbnail` | JPEG 缩略图，`size=sm`（320px）/ `lg`（1600px），生成后缓存在 `cache/` 下 |
| `GET /api/v1/datasets/{id}/labels` | 标签分布：每个标签的目标数与图片数 |
| `POST /api/v1/datasets/{id}/scan` | 重新扫描数据集文件夹，与磁盘同步（新增、变化、删除的图片与标注） |
| `GET /api/v1/datasets/{id}/imports` | 导入 / 扫描记录（新的在前）；可按 `annotation_format`、`status` 筛选，`limit` 默认 20 |
| `POST /api/v1/datasets/{id}/imports` | 创建导入任务：`{source: "archive" \| "folder", file_name, file_size, file_count?, on_conflict?: "skip" \| "overwrite", annotation_format?: "voc" \| "xanylabeling" \| "yolo", classes?: [...]}`（`yolo` 时 `classes` 必填，见“标注格式”） |
| `GET /api/v1/datasets/{id}/imports/{importId}` | 导入任务状态与进度（前端轮询） |
| `PUT /api/v1/datasets/{id}/imports/{importId}/archive?offset=N` | 上传压缩包的一个分块（原始字节，≤ 64 MiB），`offset` 必须等于已接收字节数，支持断点续传 |
| `POST /api/v1/datasets/{id}/imports/{importId}/files` | 上传文件夹中的一批文件（multipart，每个文件前带一个 `path` 字段给出相对路径，单次 ≤ 256 MiB） |
| `POST /api/v1/datasets/{id}/imports/{importId}/commit` | 上传完成，进入后台处理队列 |
| `POST /api/v1/datasets/{id}/imports/{importId}/cancel` | 取消上传或处理 |
| `POST /api/v1/datasets/{id}/exports` | 准备下载：`{format: "voc" \| "xanylabeling" \| "yolo", classes?: [...]}`（`yolo` 时必填），返回 201 `{url, file_name, expires_at}`；数据集没有图片或文件夹不存在时 409 |
| `GET /api/v1/datasets/{id}/exports/{token}` | 下载 zip（`Content-Disposition: attachment`，流式输出，无 Content-Length）；链接 1 小时内可重复使用，失效后 404 |

### 项目接口

项目按检测任务组织数据集；项目与数据集是多对多关系（表 `project_datasets`），一个数据集可以属于多个项目。字段定义以前端 `frontend/src/features/projects/api.ts` 中的类型为准（与后端 `internal/httpapi/projects.go` 保持一致）。`{id}` 等路径参数必须是正整数，否则返回 404。

| 方法与路径 | 说明 |
|---|---|
| `GET /api/v1/projects` | 项目列表（`q` 按名称 / 描述筛选），按更新时间倒序。每项带其中数据集的合计统计（`dataset_count`、`image_count`、`annotated_count`、`empty_count`、`invalid_count`、`object_count`、`total_bytes`）和封面图片（最多 4 张，轮流取自项目中最早纳入的几个数据集，每项带 `dataset_id`） |
| `POST /api/v1/projects` | `{name, description?, dataset_ids?}` 新建项目，可同时纳入数据集，返回 201。名称不区分大小写唯一（重名 409 `conflict`），最长 64 个字符，不能含控制字符；描述最长 500 个字符，除换行与制表符外不能含控制字符；不合规时返回 422。所选数据集不存在时返回 422，不创建项目 |
| `GET /api/v1/projects/{id}` | 项目详情（字段同列表项） |
| `PATCH /api/v1/projects/{id}` | 修改名称和描述（规则同上） |
| `DELETE /api/v1/projects/{id}` | 删除项目，返回 204。只删除项目本身，其中的数据集保留 |
| `GET /api/v1/projects/{id}/datasets` | 项目中的数据集，格式同数据集列表。项目不存在时返回 404（`GET /api/v1/datasets?project_id=` 则返回空列表） |
| `POST /api/v1/projects/{id}/datasets` | `{dataset_ids: [...]}` 把数据集纳入项目（已在项目中的忽略），返回更新后的项目；`dataset_ids` 为空或有数据集不存在时返回 422，不做任何修改 |
| `DELETE /api/v1/projects/{id}/datasets/{datasetId}` | 把数据集移出项目，返回 204（数据集本身不受影响；本来就不在项目中也返回 204） |
| `GET /api/v1/projects/{id}/labels` | 项目中所有数据集合计的标签分布：每个标签的目标数、图片数与所在的数据集数；项目不存在时返回 404 |

一次最多提交 1000 个 ID。增减数据集（包括删除其中的数据集）会更新项目的 `updated_at`；项目与数据集之间的写操作都按“先数据集、后项目，各自按 ID 顺序”加行锁，并发修改同一关系时不会死锁或丢失更新。删除数据集或项目时，关联记录随之级联删除。训练任务与模型上线后也将归属到项目，项目详情页的“模型”目前是占位。

JSON 接口都带 `Cache-Control: no-store`；原图与缩略图带 `?v=<版本>` 请求时返回 `Cache-Control: private, max-age=31536000, immutable`（文件变化后版本号随之改变），否则为 `private, no-cache`。

### 中间件

按执行顺序：

1. **RequestID**：见上文。
2. **客户端 IP**：只在设置了 `TM_TRUSTED_PROXIES`（反向代理的 IP / CIDR）时启用：对端是受信代理时，从右往左取 `X-Forwarded-For` 中第一个非受信地址作为客户端 IP。未设置时忽略该请求头，客户端 IP 就是 TCP 对端地址，无法伪造。目前只用于访问日志；以后用于操作审计等场景时，部署方必须只把真正的代理列入 `TM_TRUSTED_PROXIES`。
3. **访问日志**：method、path、status、bytes、duration_ms、client_ip、request_id。5xx（501 除外）记为 ERROR；健康检查记为 DEBUG，避免刷屏。
4. **panic 恢复**：记录堆栈并返回 500 错误信封。

### 前端静态文件

设置 `TM_WEB_DIR` 后，非 `/api` 路径由 `internal/web` 处理：

- 存在的文件直接返回；不存在的路径回退到 `index.html`，以支持前端路由（如刷新 `/datasets/versions`）。
- `/assets/*`（Vite 生成的带内容哈希的文件）返回 `Cache-Control: public, max-age=31536000, immutable`；`index.html` 等其它文件返回 `no-cache`。
- `/assets/` 下不存在的文件返回 404，而不是回退 `index.html`，避免浏览器把 HTML 当作 JS/CSS 缓存。
- 不生成目录列表；路径先经 `path.Clean` 再交给 `fs.FS`，无法访问目录之外的文件。
- 只允许 `GET` / `HEAD`。

## 4. 前端结构

前端位于 `frontend/`（Vite + React + TypeScript + Tailwind CSS v4 + shadcn/ui）。

- `src/config/navigation.ts` 是导航与模块的**唯一配置源**：侧边栏、路由、面包屑和占位页文案都由它生成。
- `src/router.tsx` 中的 `pages` 把每个模块 id 映射到页面组件。
- `src/features/<领域>/` 下每个模块有独立的页面文件；规划中的模块渲染通用占位组件 `ModulePlaceholder`，实现功能时直接替换该文件内容即可。
- `src/lib/api.ts` 提供 `apiGet<T>()` 和 `apiSend<T>(method, path, body)`，非 2xx 时解析错误信封并抛出 `ApiError(status, code, message)`；网络错误转换为 `code = "network_error"`。
- 服务端状态用 TanStack Query 管理，界面组件来自 `src/components/ui/`（shadcn/ui）。
- 项目模块位于 `src/features/projects/`：项目列表页（卡片 / 列表视图）、项目详情页（数据集 / 标签分布 / 模型占位，当前标签页记在 URL 的 `?tab=`）、新建 / 编辑 / 删除对话框、添加数据集对话框，以及数据集页使用的“纳入项目”对话框和项目标记。数据集页的项目筛选记在 URL 的 `?project=<项目ID|none>`。
- 数据集模块位于 `src/features/datasets/`：列表页、详情页（图片 / 标签分布 / 导入记录）、新建与导入对话框、图片查看器（叠加 VOC 标注框）。上传由全局的上传管理器（`upload-manager.ts`，`useSyncExternalStore`）执行，切换页面不会中断；右下角的上传面板显示进度，上传中关闭页面会弹出提醒。

开发时由 Vite（:5173）托管前端并把 `/api` 代理到 Go 服务（:8080）；生产环境由 Go 服务托管 `frontend/dist`。

## 5. 存储布局

`TM_DATA_DIR`（容器内 `/data`，绑定挂载宿主机 `TM_HOST_STORAGE_DIR`）下的固定子目录：

| key | 子目录 | 用途 |
|---|---|---|
| `datasets` | `datasets/` | 数据集文件夹：每个数据集一个子目录，图片与 VOC 标注直接以文件保存（见第 6 节） |
| `models` | `models/` | 模型权重文件 |
| `runs` | `runs/` | 训练 / 评估运行产物（日志、曲线、中间权重） |
| `exports` | `exports/` | 导出的训练集（YOLO / VOC / COCO）、打包导出与报告 |
| `cache` | `cache/` | 缩略图等可再生的缓存，可随时删除，不需要备份 |
| `tmp` | `tmp/` | 上传、解压等临时文件 |

平台框架阶段的 `images/` 目录已不再使用（从未写入过数据），旧部署中留下的空目录可以直接删除。

后续功能请遵循：

- **数据库里只存相对路径**（如数据集记录 `datasets.dir_name` 相对于 `datasets/`，图片 `dataset_images.rel_path` 相对于数据集文件夹），不存绝对路径，这样数据目录可以整体搬迁。
- 由相对路径得到磁盘路径一律使用 `storage.Resolve(key, rel)`，它会拒绝绝对路径和 `..` 越界。该检查是字面上的，打开用户可控路径时建议再配合 `os.OpenRoot`。
- 上传先写入 `tmp/`，完成并校验后再 `os.Rename` 到目标目录（同一文件系统内是原子操作），避免出现半个文件。
- 新增顶层目录时，在 `internal/storage` 的 `Keys` 中登记，并同步修改 `deploy/docker/entrypoint.sh` 中的目录列表与本文。

## 6. 数据集与导入

### 存储方式

- 每个数据集对应 `datasets/` 下的一个文件夹（容器内 `/data/datasets/<名称>/`，宿主机默认 `./data/storage/datasets/<名称>/`）。通过网页上传创建的数据集，文件夹名就是数据集名称；文件夹内保留上传时的子目录结构。
- **文件系统是数据的来源，数据库是索引**：`dataset_images` 记录每张图片的相对路径、大小、修改时间、尺寸、匹配到的 VOC 文件和各标签的目标数。用户可以直接在服务器上增删文件，再在页面上点“重新扫描”同步。
- 也可以把整理好的文件夹直接拷贝到 `datasets/` 下，在页面上“添加已有目录”：只建立索引，不移动、不复制文件。
- 数据集名称规则（前后端一致，见 `internal/dataset/naming.go` 与 `frontend/src/features/datasets/naming.ts`）：1–64 个字符，不能包含 `\ / : * ? " < > |` 和控制字符，不能以 `.` 开头、以 `.` 或空格结尾，不能是 Windows 保留名称（`CON`、`NUL`、`COM1` 等）；名称不区分大小写唯一。修改名称不会重命名文件夹。

### 文件识别

- 图片：`.jpg .jpeg .png .bmp .gif .webp .tif .tiff .avif`（不区分大小写）。尺寸通过读取文件头获得（AVIF 无法读取时为空）。
- VOC 标注：与图片**同目录、同名**的 `.xml`（如 `NG/0001.jpg` ↔ `NG/0001.xml`）；也识别标准 Pascal VOC 目录结构 `JPEGImages/0001.jpg` ↔ `Annotations/0001.xml`。根元素必须是 `<annotation>`；XML 声明为 GBK / GB2312 / GB18030 等编码时自动转换。
- 标注状态：含至少一个目标为“已标注”；有 VOC 文件但没有目标为“空标注”（通常是负样本）；配对到的 XML 无法解析或不是 VOC 格式为“标注无效”（可单独筛选，查看图片时显示具体原因）；没有 VOC 文件为“未标注”。
- 忽略：隐藏文件与目录（以 `.` 开头，含 macOS 的 `._*`）、`__MACOSX`、`Thumbs.db`、`desktop.ini`，以及其它非图片、非 XML 的文件（上传 X-AnyLabeling / YOLO 标注时，保留的是 `.json` / `.txt`，见下方“标注格式”）；符号链接一律跳过。文件名不是合法 UTF-8 的文件或文件夹（例如在 Linux 上用不支持 GBK 的工具解压出的乱码文件名）会被跳过并计入结果中的 `names_invalid`，需要在服务器上重命名后重新扫描。

### 导入流程

| 来源 | 上传 | 后台处理 |
|---|---|---|
| 压缩包（`source=archive`） | 浏览器按 8 MiB 分块上传到 `tmp/imports/<任务ID>/`，可断点续传 | 按文件头识别格式（zip、tar、tar.gz / bz2 / xz / zst、7z、rar）→ 解压图片与所选格式的标注文件（zip 中未标记 UTF-8 的文件名按 GB18030 解码；跳过 `..`、绝对路径、符号链接等不安全条目）→ 所有文件位于同一个顶层文件夹时去掉这一层 →（X-AnyLabeling / YOLO）转换为 VOC → 配对标注 → 移入数据集文件夹并建立索引 |
| 文件夹（`source=folder`） | 浏览器分批（每批 ≤ 64 个文件或 32 MiB，3 路并发）上传图片与所选格式的标注文件，路径相对于所选文件夹 |（X-AnyLabeling / YOLO）转换为 VOC → 配对标注 → 移入数据集文件夹并建立索引 |
| 扫描（`source=scan`） | — | 遍历数据集文件夹，与索引比对：新增、变化（大小 / 修改时间 / 标注变化）的文件重新解析，已删除的文件移除索引 |

- 上传时选择压缩包 / 文件夹中标注文件的格式（`annotation_format`）：`voc`（默认）、`xanylabeling` 或 `yolo`；YOLO 需要同时提供类别顺序（`classes`）。转换规则见下方“标注格式”。
- 导入到已有数据集时可选择同名文件的处理方式：`skip`（默认，保留已有文件）或 `overwrite`。
- 任务状态：`uploading` → `queued` → `running` → `succeeded` / `failed` / `canceled`，进度与结果摘要（新增、跳过、匹配标注、无效标注、忽略文件等）写入 `dataset_imports`，前端轮询显示。
- 开始上传前检查磁盘剩余空间（压缩包需要约 2 倍空间用于解压）；解压时也会持续检查实际剩余空间（始终保留 512 MiB），防止压缩炸弹或同时进行的多个导入写满磁盘。xz / zstd / rar 的解压字典大小有上限，避免构造的压缩包耗尽内存。
- 任务执行器在进程内运行（2 个 worker，同一数据集的任务串行执行）。服务重启时：执行中的任务标记为失败（已移入数据集的文件可以通过“重新扫描”纳入），排队中的任务重新入队；超过 24 小时未完成的上传自动取消并清理临时文件。
- 因为任务执行器在进程内，**目前只支持单实例部署**（一个 app 容器）。

### 标注格式

数据集在服务器上**始终以 VOC 保存**（图片 + 同名 `.xml`）。X-AnyLabeling 与 YOLO 标注只在上传时转换、下载时生成（`internal/dataset/formats.go`、`convert.go`、`export.go`，前端镜像规则在 `frontend/src/features/datasets/files.ts` 与 `class-list.ts`）。

**类别顺序**（YOLO 导入、导出都需要）：类别名称列表，第 1 个是类别 0。页面上每行一个类别，可直接输入或读取 `classes.txt`；去掉首尾空白与首尾空行，中间不能有空行，名称不能为空、不能重复（区分大小写）、不能含控制字符、不超过 255 个字符，最多 1000 个。

**上传时的配对与转换**（在临时目录中完成，之后与 VOC 上传的流程相同）：

| 格式 | 上传 / 解压的标注文件 | 与图片配对 | 转换 |
|---|---|---|---|
| X-AnyLabeling（`xanylabeling`） | `.json`（X-AnyLabeling / LabelMe 格式，根对象含 `shapes`） | 同目录同名（先精确，再忽略大小写）；否则按 JSON 中的 `imagePath`（相对于 JSON 所在目录）；多张图片争同一个 JSON 时按 `imagePath` 的文件名决定 | `rectangle`、`rotation`、`quadrilateral`、`polygon`、`cuboid` 取点的外接矩形，`circle` 取外接正方形；`point`、`line`、`linestrip` 等无法表示为矩形框的形状跳过（`objects_skipped`）。保留 `difficult` |
| YOLO（`yolo`） | `.txt`（`classes.txt` 除外） | 同目录同名；否则 Ultralytics 布局：路径中最后一个 `images` 目录换成 `labels`（如 `images/train/a.jpg` ↔ `labels/train/a.txt`，忽略大小写） | 每行 `class cx cy w h`（归一化，第 6 列置信度忽略）或多边形 `class x1 y1 x2 y2 x3 y3 …`（分割 / OBB，取外接矩形）；空文件为空标注（负样本）。按图片实际尺寸换算为像素 |

- 转换结果写成图片同目录的 `<图片名>.xml`（`<filename>`、`<size>` 取图片实际值），坐标裁剪到图片范围并取整，宽或高为 0 的框跳过；原始 json / txt 不保存到数据集中，非所选格式的标注文件（如选了 YOLO 时的 `.xml`）也不导入。
- 无法解析的标注文件（不是 JSON / LabelMe 格式、YOLO 行格式错误或坐标不是 0–1 的归一化值、YOLO 读不出图片尺寸）计入 `annotations_failed`，对应图片按“未标注”导入（覆盖模式下替换已有图片时，数据集中原有的同名 `.xml` 一并删除，因为它属于被替换的图片）；没有对应图片的标注文件计入 `annotations_orphaned`。
- YOLO 标注中出现超出类别列表的类别编号时，**整个导入任务失败**，数据集不做任何改动，错误信息指出文件、行号和类别数，修正类别顺序后重新导入即可。
- 结果摘要另有 `annotations_converted`（成功转换的标注文件数）；处理阶段多一个 `converting`（正在转换标注）。

**下载**（`POST …/exports` 生成 1 小时内有效、可重复使用的下载链接，`GET` 该链接时边打包边下载，不占用额外磁盘空间；浏览器直接保存，数据集再大也不经过内存）。压缩包为 zip（JPEG / PNG 等已压缩的图片不再压缩，BMP / TIFF 与标注文件用 Deflate），所有文件位于与数据集文件夹同名的顶层文件夹中：

| 格式 | 内容 |
|---|---|
| VOC | 图片与配对的 `.xml` 按原路径原样复制 |
| X-AnyLabeling | 图片按原路径；有效标注（已标注 / 空标注）的图片在同目录生成 `<名称>.json`（X-AnyLabeling 4.x 格式，矩形为 4 个点，`imageData` 为 null） |
| YOLO | Ultralytics 布局：`images/…`（保持子目录；图片原本就全在 `images/` 下时不再加一层）、`labels/…`（`class cx cy w h`，6 位小数）、`classes.txt`、`data.yaml`（检测到 `images/train`、`images/val`（或 `valid`）、`images/test` 时按此划分，否则 train 与 val 都指向全部图片）。不在类别列表中的标签不导出；有标注文件但目标全部被排除的图片生成空 txt |

下载为 YOLO 时，页面上的类别顺序默认沿用该数据集最近一次成功的 YOLO 导入所用的顺序（类别编号与导入时一致），数据集中其余的标签按名称排在后面；没有 YOLO 导入时按标签名称排序。

未标注或标注无法解析的图片只包含图片本身；扫描之后已被删除的图片会被跳过。两个文件在压缩包中的名称相同时（如 `a.jpg` 与 `a.png` 的 `a.json`），只保留第一个。打包中途出错时连接被中断，浏览器会显示下载失败，而不会留下不完整的压缩包。

### 缩略图

缩略图在第一次请求时生成（`sm` 320px 用于网格，`lg` 1600px 用于查看器），保存为 `cache/thumbs/<数据集ID>/<图片ID>-<尺寸>-<版本>.jpg`。同时生成的数量按 CPU 核数和预估内存占用（按图片格式估算解码所需内存，如 JPEG 的采样方式与渐进式编码）限制，相同请求只生成一次；像素过多或长宽比极端、解码缩放所需内存过大的图片不生成缩略图（返回 `unsupported_image`，前端回退为原图或占位图）。文件变化后版本号改变，会生成新的缩略图。删除 `cache/` 不影响数据，缩略图会按需重新生成。

## 7. 数据库迁移约定

- 迁移文件放在 `backend/internal/database/migrations/`，命名 `NNNN_描述.sql`（如 `0002_create_datasets.sql`），版本号从文件名解析，必须为正整数且不能重复（单元测试会检查编号连续）。
- 文件通过 `go:embed` 编译进二进制，启动时自动执行（`TM_DB_AUTO_MIGRATE=false` 可关闭）。
- 执行器使用 `schema_migrations(version, name, applied_at)` 记录已执行的版本；用 `pg_advisory_lock` 防止多个实例并发迁移；每个文件在**一个事务**中执行并记录。
- 因此迁移文件中**不要写 `BEGIN` / `COMMIT`**，也不能包含无法在事务中执行的语句（如 `CREATE INDEX CONCURRENTLY`）。
- 迁移只向前：**已发布的迁移文件不要修改，也不要改名**，需要变更就新增一个文件。
- 同一个版本号只能对应一个文件。执行器会比对 `schema_migrations` 中记录的文件名：数据库里已执行过的版本号在当前代码中对应另一个文件名时（典型情况是两个分支各自新增了 `0002_*.sql`，又共用一个开发数据库），启动直接失败并给出两个文件名，而不是悄悄跳过。此时给后合入的那个文件换一个新编号；开发数据库可以删掉 `data/postgres` 重建。
- `0001_init.sql` 只建了基础设施表 `app_settings(key, value jsonb, updated_at)`；`0002_create_datasets.sql` 建立数据集相关的 `datasets`、`dataset_images`、`dataset_imports`；`0003_create_projects.sql` 建立 `projects` 和项目与数据集的关联表 `project_datasets`。其它业务表（标注、训练任务等）随对应功能一起以新迁移加入，不预先设计。

## 8. 日志

- 使用 `log/slog`，开发时默认 text 格式，容器中为 JSON（`TM_LOG_FORMAT=json`），输出到 stderr，由 Docker 收集（`docker compose logs app`）。
- 级别由 `TM_LOG_LEVEL` 控制（debug / info / warn / error）。
- 日志字段名使用英文 snake_case；面向用户的提示（API 的 `message`、界面文案）使用中文。

## 9. 训练节点规划（未实现）

训练需要 GPU 和 Python 生态（PyTorch / Ultralytics 等），计划作为**独立的 worker 服务**接入，而不是放进 Go 服务：

- **镜像**：单独的 `trainer` 镜像（Python + CUDA），`docker-compose.yml` 末尾已以注释形式预留服务示例（`deploy.resources.reservations.devices` 使用 nvidia 驱动）。宿主机需安装 NVIDIA 驱动与 NVIDIA Container Toolkit。
- **任务分发**：训练任务记录在 PostgreSQL 中；worker 主动通过 HTTP API 领取任务（服务端用 `SELECT … FOR UPDATE SKIP LOCKED` 保证一个任务只被一个 worker 领取），并定期上报心跳、进度、日志与指标。Go 服务不直接调度容器。
- **数据交换**：worker 与 app 挂载同一个存储目录，从 `datasets/` 读取导出的训练集，把运行产物写入 `runs/`、最终权重写入 `models/`；训练节点在其他机器上时，通过 NFS 等共享存储挂载同一目录。
- **扩展**：一台服务器可以运行多个 worker（每个绑定不同 GPU），也可以在其他 GPU 机器上单独部署 worker，“计算资源”页面将展示已注册节点与 GPU 状态。
- 评估、批量推理、预标注等耗时任务也将复用同一套 worker 与任务队列机制。
