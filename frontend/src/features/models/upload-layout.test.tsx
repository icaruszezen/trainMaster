// 上传面板中的行与上传对话框：服务端消息里不能断开的长名称（yaml 文件名、
// Python 对象名）要在原地换行，不能把面板或对话框撑宽。
import { renderToStaticMarkup } from "react-dom/server"
import { MemoryRouter } from "react-router"
import { describe, expect, it } from "vitest"

import { Dialog } from "@/components/ui/dialog"
import { ModelUploadTaskItem } from "@/features/models/model-upload-item"
import type { ModelUploadTask } from "@/features/models/upload-manager"
import { UploadTaskView } from "@/features/models/upload-model-dialog"
import { autoColumnGrids, enclosingClasses } from "@/lib/test-markup"

const yaml = "yolov8s_custom_coordinate_attention_neck_p2_head.yaml"
const mismatch = `所选的版本是 YOLO11，但文件中的模型是 YOLOv8（${yaml}）。请改选 YOLOv8 后再保存`
const unsafe =
  "文件中引用了平台不允许加载的对象（my_project.models.attention.CoordinateAttentionModule），" +
  "为了安全不会使用它。请上传由 Ultralytics 训练保存的模型文件"

function task(patch: Partial<ModelUploadTask>): ModelUploadTask {
  return {
    id: "model:7",
    uploadId: 7,
    projectId: 3,
    projectName: "A 线外观检测",
    fileName: "helmet_detection_yolo11s_best_20261103.pt",
    totalBytes: 19_000_000,
    uploadedBytes: 19_000_000,
    speed: 0,
    status: "mismatch",
    yoloVersion: "yolo11",
    detectedVersion: null,
    error: "",
    errorCode: "",
    retryable: false,
    model: null,
    createdAt: 0,
    ...patch,
  }
}

const cases: [string, ModelUploadTask][] = [
  [
    "version mismatch",
    task({ status: "mismatch", detectedVersion: "yolov8", error: mismatch }),
  ],
  [
    "version mismatch without a detected version",
    task({ status: "mismatch", error: mismatch }),
  ],
  [
    "invalid model",
    task({ status: "error", errorCode: "invalid_model", error: unsafe }),
  ],
  [
    "retryable failure",
    task({ status: "error", retryable: true, error: unsafe }),
  ],
]

describe("ModelUploadTaskItem", () => {
  it.each(cases)("wraps a long %s message within the tray", (_, item) => {
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <ModelUploadTaskItem task={item} />
      </MemoryRouter>
    )

    const classes = enclosingClasses(html, item.error)
    expect(classes).not.toBeNull()
    expect(autoColumnGrids(classes ?? [])).toEqual([])
    expect(classes?.at(-1)).toEqual(
      expect.arrayContaining(["min-w-0", "break-words"])
    )
  })
})

describe("UploadTaskView", () => {
  it.each(cases)("wraps a long %s message within the dialog", (_, item) => {
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <Dialog open>
          <UploadTaskView
            task={item}
            onClose={() => {}}
            onUploadAnother={() => {}}
            onChooseFile={() => {}}
            onRestart={() => {}}
          />
        </Dialog>
      </MemoryRouter>
    )

    // 消息在 Alert 的 1fr 列中（最小宽度是内容宽度），break-words 不会降低
    // 最小宽度，只有 wrap-anywhere 能让长名称换行。
    const classes = enclosingClasses(html, item.error)
    expect(classes).not.toBeNull()
    expect(classes?.at(-1)).toContain("wrap-anywhere")
  })
})
