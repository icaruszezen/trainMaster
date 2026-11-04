import { useId, useRef, useState, type ReactNode } from "react"
import { CircleAlert, Info, Trash2, TriangleAlert } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import type { ClassOption } from "@/features/annotation/classes"
import type { EditorObject } from "@/features/annotation/document"
import {
  boxHeight,
  boxWidth,
  normalizeBox,
  type Size,
} from "@/features/annotation/geometry"
import { focusCanvas, preventTypeahead } from "@/features/annotation/shortcuts"
import type {
  AnnotationObject,
  DatasetImageDetail,
} from "@/features/datasets/api"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

/** 侧边面板中的一节：小标题加内容（`action` 放在标题右侧）。 */
export function PanelSection({
  title,
  action,
  children,
  className,
}: {
  title: ReactNode
  action?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={cn("grid gap-2", className)}>
      <div className="flex min-h-7 items-center justify-between gap-2">
        <h2 className="text-xs font-medium text-muted-foreground">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  )
}

type ObjectPanelProps = {
  objects: EditorObject[]
  selected: string | null
  /** 框的类别下拉框中的候选（见 `buildClassOptions`）。 */
  options: readonly ClassOption[]
  colorFor: (name: string) => string
  size: Size | null
  readOnly: boolean
  onSelect: (uid: string) => void
  onHighlight: (uid: string | null) => void
  onChange: (uid: string, patch: Partial<AnnotationObject>) => void
  onDelete: (uid: string) => void
}

/** 本图的框：列表（单击选中、悬停突出显示）与选中框的属性、精确坐标。 */
export function ObjectPanel({
  objects,
  selected,
  options,
  colorFor,
  size,
  readOnly,
  onSelect,
  onHighlight,
  onChange,
  onDelete,
}: ObjectPanelProps) {
  const selectedObject = objects.find((object) => object.uid === selected)
  return (
    <PanelSection
      title={`本图的框（${formatNumber(objects.length)}）`}
      className="min-h-0"
    >
      {objects.length === 0 ? (
        <p className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          {readOnly
            ? "没有框。"
            : "还没有框。按 W 后在图片上拖动（或先后点击两个对角）画框，再选择或输入它的类别。"}
        </p>
      ) : (
        <ol
          className="grid max-h-72 gap-0.5 overflow-y-auto"
          onMouseLeave={() => onHighlight(null)}
        >
          {objects.map((object, index) => (
            <li key={object.uid}>
              <button
                type="button"
                onClick={() => onSelect(object.uid)}
                onMouseEnter={() => onHighlight(object.uid)}
                onFocus={() => onHighlight(object.uid)}
                onBlur={() => onHighlight(null)}
                aria-pressed={object.uid === selected}
                className={cn(
                  "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50",
                  object.uid === selected
                    ? "bg-accent text-accent-foreground"
                    : "hover:bg-muted"
                )}
              >
                <span className="w-5 text-right text-xs text-muted-foreground tabular-nums">
                  {index + 1}
                </span>
                <span
                  className="size-2.5 shrink-0 rounded-sm"
                  style={{ backgroundColor: colorFor(object.name) }}
                  aria-hidden
                />
                <span className="min-w-0 flex-1 truncate">{object.name}</span>
                {object.difficult && (
                  <Badge variant="outline" className="h-5 px-1 text-[10px]">
                    难例
                  </Badge>
                )}
                {object.truncated && (
                  <Badge variant="outline" className="h-5 px-1 text-[10px]">
                    截断
                  </Badge>
                )}
                <span className="text-xs text-muted-foreground tabular-nums">
                  {boxWidth(object)}×{boxHeight(object)}
                </span>
              </button>
            </li>
          ))}
        </ol>
      )}
      {selectedObject && (
        <ObjectDetails
          key={selectedObject.uid}
          object={selectedObject}
          options={options}
          colorFor={colorFor}
          size={size}
          readOnly={readOnly}
          onChange={(patch) => onChange(selectedObject.uid, patch)}
          onDelete={() => onDelete(selectedObject.uid)}
        />
      )}
    </PanelSection>
  )
}

function ObjectDetails({
  object,
  options,
  colorFor,
  size,
  readOnly,
  onChange,
  onDelete,
}: {
  object: EditorObject
  options: readonly ClassOption[]
  colorFor: (name: string) => string
  size: Size | null
  readOnly: boolean
  onChange: (patch: Partial<AnnotationObject>) => void
  onDelete: () => void
}) {
  // id 不写死：页面上有多份面板（如切换布局时）也不会重复，标签对应各自的控件。
  const id = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  // 框自己的类别总在下拉框中（候选中没有时加在最后）。
  const candidates = options.map((option) => option.name)
  const names = candidates.includes(object.name)
    ? candidates
    : [...candidates, object.name]
  return (
    <div className="grid gap-3 rounded-md border bg-muted/30 p-3">
      <div className="grid gap-1.5">
        <Label
          htmlFor={`${id}-class`}
          className="text-xs text-muted-foreground"
        >
          类别
        </Label>
        <Select
          value={object.name}
          onValueChange={(name) => onChange({ name })}
          disabled={readOnly}
        >
          <SelectTrigger
            ref={triggerRef}
            id={`${id}-class`}
            size="sm"
            className="w-full"
            onKeyDown={preventTypeahead}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent
            onCloseAutoFocus={(event) => {
              // 下拉框关闭后（选了类别、按 Esc、点击别处、选了原来的类别）都把
              // 焦点还给画布：焦点留在下拉框上时快捷键不能用。
              if (focusCanvas(triggerRef.current)) {
                event.preventDefault()
              }
            }}
          >
            {names.map((name) => (
              <SelectItem key={name} value={name}>
                <span
                  className="size-2.5 rounded-sm"
                  style={{ backgroundColor: colorFor(name) }}
                  aria-hidden
                />
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <CoordinateFields
        object={object}
        size={size}
        readOnly={readOnly}
        onChange={onChange}
      />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <FlagField
          id={`${id}-difficult`}
          label="难例（difficult）"
          shortcut="Q"
          checked={object.difficult}
          disabled={readOnly}
          onChange={(difficult) => onChange({ difficult })}
        />
        <FlagField
          id={`${id}-truncated`}
          label="截断（truncated）"
          shortcut="E"
          checked={object.truncated}
          disabled={readOnly}
          onChange={(truncated) => onChange({ truncated })}
        />
      </div>
      {!readOnly && (
        <Button
          variant="outline"
          size="sm"
          onClick={onDelete}
          className="text-destructive hover:text-destructive"
        >
          <Trash2 />
          删除这个框
          <Kbd className="ml-auto">Delete</Kbd>
        </Button>
      )}
    </div>
  )
}

function FlagField({
  id,
  label,
  shortcut,
  checked,
  disabled,
  onChange,
}: {
  id: string
  label: string
  shortcut: string
  checked: boolean
  disabled: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-center gap-2">
      <Checkbox
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={(value) => onChange(value === true)}
      />
      <Label htmlFor={id} className="text-sm font-normal">
        {label}
        <Kbd>{shortcut}</Kbd>
      </Label>
    </div>
  )
}

const COORDINATES = ["xmin", "ymin", "xmax", "ymax"] as const
type Coordinate = (typeof COORDINATES)[number]

function CoordinateFields({
  object,
  size,
  readOnly,
  onChange,
}: {
  object: EditorObject
  size: Size | null
  readOnly: boolean
  onChange: (patch: Partial<AnnotationObject>) => void
}) {
  const [drafts, setDrafts] = useState<Partial<Record<Coordinate, string>>>({})
  // Esc 取消输入时失去焦点，这次失去焦点不应用输入的值。
  const cancelling = useRef(false)
  const cancel = (field: Coordinate) =>
    setDrafts((current) => {
      const next = { ...current }
      delete next[field]
      return next
    })
  const commit = (field: Coordinate) => {
    const text = drafts[field]
    setDrafts((current) => {
      const next = { ...current }
      delete next[field]
      return next
    })
    if (text === undefined || !size) {
      return
    }
    const value = Number(text)
    if (!Number.isFinite(value)) {
      return
    }
    const limit = field.startsWith("x") ? size.width : size.height
    const box = normalizeBox(
      {
        xmin: object.xmin,
        ymin: object.ymin,
        xmax: object.xmax,
        ymax: object.ymax,
        [field]: Math.min(limit, Math.max(0, Math.round(value))),
      },
      size
    )
    onChange(box)
  }
  return (
    <div className="grid grid-cols-4 gap-1.5">
      {COORDINATES.map((field) => (
        <label key={field} className="grid gap-1">
          <span className="text-[11px] text-muted-foreground">{field}</span>
          <Input
            value={drafts[field] ?? String(object[field])}
            inputMode="numeric"
            disabled={readOnly || !size}
            onChange={(event) =>
              setDrafts({ ...drafts, [field]: event.target.value })
            }
            onBlur={() => {
              if (!cancelling.current) {
                commit(field)
              }
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                commit(field)
              } else if (event.key === "Escape") {
                // 恢复原来的值，不应用输入的内容。
                cancel(field)
                cancelling.current = true
                event.currentTarget.blur() // 同步触发 onBlur
                cancelling.current = false
              }
            }}
            className="h-7 px-1.5 text-xs tabular-nums md:text-xs"
          />
        </label>
      ))}
    </div>
  )
}

type ImageInfoProps = {
  detail: DatasetImageDetail
  size: Size | null
  notices: Notice[]
}

export type Notice = {
  tone: "info" | "warning" | "error"
  message: ReactNode
  action?: ReactNode
}

/** 图片与标注文件的信息，以及需要注意的情况（只读原因、无法解析等）。 */
export function ImageInfo({ detail, size, notices }: ImageInfoProps) {
  const stateText: Record<DatasetImageDetail["annotation_state"], string> = {
    none: "未标注（没有标注文件）",
    empty: "空标注（没有目标，负样本）",
    annotated: "已标注",
    invalid: "标注文件无法解析",
  }
  return (
    <PanelSection title="图片">
      <dl className="grid gap-1.5 text-xs">
        <InfoRow label="路径">
          <span className="font-mono break-all">{detail.path}</span>
        </InfoRow>
        <InfoRow label="尺寸">
          {size ? (
            <span className="tabular-nums">
              {formatNumber(size.width)} × {formatNumber(size.height)}
            </span>
          ) : (
            "未知"
          )}
        </InfoRow>
        <InfoRow label="状态">{stateText[detail.annotation_state]}</InfoRow>
        {detail.annotation_path && (
          <InfoRow label="标注文件">
            <span className="font-mono break-all">
              {detail.annotation_path}
            </span>
          </InfoRow>
        )}
      </dl>
      {notices.map((notice, index) => (
        <NoticeBox key={index} notice={notice} />
      ))}
    </PanelSection>
  )
}

export function NoticeBox({ notice }: { notice: Notice }) {
  const Icon =
    notice.tone === "error"
      ? CircleAlert
      : notice.tone === "warning"
        ? TriangleAlert
        : Info
  return (
    <div
      className={cn(
        "grid gap-2 rounded-md border px-3 py-2 text-xs",
        notice.tone === "error" &&
          "border-destructive/30 bg-destructive/5 text-destructive",
        notice.tone === "warning" &&
          "border-amber-500/40 bg-amber-500/5 text-amber-800 dark:text-amber-400",
        notice.tone === "info" && "bg-muted/40 text-muted-foreground"
      )}
    >
      <p className="flex items-start gap-1.5 break-words">
        <Icon className="mt-px size-3.5 shrink-0" />
        <span className="min-w-0">{notice.message}</span>
      </p>
      {notice.action}
    </div>
  )
}

/** 信息列表（`<dl>`）中的一行。 */
export function InfoRow({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <div className="grid grid-cols-[4rem_minmax(0,1fr)] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  )
}
