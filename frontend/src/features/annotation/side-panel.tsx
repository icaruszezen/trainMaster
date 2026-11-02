import { useId, useRef, useState, type ReactNode } from "react"
import {
  CircleAlert,
  Eye,
  EyeOff,
  Info,
  Plus,
  Settings2,
  Trash2,
  TriangleAlert,
} from "lucide-react"

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
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import type { EditorObject } from "@/features/annotation/document"
import { countLabels } from "@/features/annotation/document"
import {
  boxHeight,
  boxWidth,
  normalizeBox,
  type Size,
} from "@/features/annotation/geometry"
import {
  classHotkey,
  focusCanvas,
  preventTypeahead,
} from "@/features/annotation/shortcuts"
import type {
  AnnotationObject,
  DatasetClass,
  DatasetImageDetail,
} from "@/features/datasets/api"
import { formatNumber } from "@/lib/format"
import { cn } from "@/lib/utils"

function Section({
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

/** 类别列表的读取状态：读取完成前不能修改类别列表（避免当作空列表覆盖）。 */
export type ClassListStatus =
  | { state: "ready" }
  | { state: "pending" }
  | { state: "error"; message: string; retrying: boolean }

type ClassPanelProps = {
  classes: DatasetClass[]
  listStatus: ClassListStatus
  onRetry: () => void
  objects: EditorObject[]
  activeClass: string | null
  colorFor: (name: string) => string
  hiddenClasses: ReadonlySet<string>
  hasSelection: boolean
  readOnly: boolean
  onPick: (name: string) => void
  onToggleHidden: (name: string) => void
  onAddToList: (name: string) => void
  onManage: () => void
}

/**
 * 类别列表：单击切换当前类别（选中框时改为修改它的类别），数字键对应前 10
 * 个类别；不在列表中的类别（如其它工具标注的）单独列出，可以加入列表。
 */
export function ClassPanel({
  classes,
  listStatus,
  onRetry,
  objects,
  activeClass,
  colorFor,
  hiddenClasses,
  hasSelection,
  readOnly,
  onPick,
  onToggleHidden,
  onAddToList,
  onManage,
}: ClassPanelProps) {
  return (
    <Section
      title={
        <>
          类别
          {hasSelection && !readOnly && (
            <span className="ml-1 font-normal">· 单击修改选中框的类别</span>
          )}
        </>
      }
      action={
        <Button
          variant="ghost"
          size="xs"
          onClick={onManage}
          disabled={listStatus.state !== "ready"}
        >
          <Settings2 />
          管理
        </Button>
      }
    >
      {listStatus.state === "pending" && (
        <div className="grid gap-1" role="status">
          <span className="sr-only">正在读取类别列表…</span>
          <Skeleton className="h-7" />
          <Skeleton className="h-7" />
          <Skeleton className="h-7" />
        </div>
      )}
      {listStatus.state === "error" && (
        <div
          role="alert"
          className="grid gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive"
        >
          <p className="break-words">无法读取类别列表：{listStatus.message}</p>
          <Button
            variant="outline"
            size="sm"
            onClick={onRetry}
            disabled={listStatus.retrying}
          >
            {listStatus.retrying && <Spinner />}
            重试
          </Button>
        </div>
      )}
      {listStatus.state === "ready" && (
        <ClassRows
          classes={classes}
          objects={objects}
          activeClass={activeClass}
          colorFor={colorFor}
          hiddenClasses={hiddenClasses}
          onPick={onPick}
          onToggleHidden={onToggleHidden}
          onAddToList={onAddToList}
          onManage={onManage}
        />
      )}
    </Section>
  )
}

function ClassRows({
  classes,
  objects,
  activeClass,
  colorFor,
  hiddenClasses,
  onPick,
  onToggleHidden,
  onAddToList,
  onManage,
}: Omit<
  ClassPanelProps,
  "listStatus" | "onRetry" | "hasSelection" | "readOnly"
>) {
  const counts = countLabels(objects)
  const known = new Set(classes.map((cls) => cls.name))
  const unknown = [...counts.keys()].filter((name) => !known.has(name)).sort()
  return (
    <>
      {classes.length === 0 && (
        <div className="grid gap-2 rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          <p>
            还没有类别。添加缺陷类别后，画框时自动使用当前类别，数字键可以快速切换。
          </p>
          <Button variant="outline" size="sm" onClick={onManage}>
            <Plus />
            添加类别
          </Button>
        </div>
      )}
      <ul className="grid gap-0.5" aria-label="类别列表">
        {classes.map((cls, index) => (
          <ClassRow
            key={cls.name}
            name={cls.name}
            color={colorFor(cls.name)}
            hotkey={classHotkey(index)}
            count={counts.get(cls.name) ?? 0}
            active={cls.name === activeClass}
            hidden={hiddenClasses.has(cls.name)}
            onPick={() => onPick(cls.name)}
            onToggleHidden={() => onToggleHidden(cls.name)}
          />
        ))}
        {unknown.map((name) => (
          <ClassRow
            key={`unknown:${name}`}
            name={name}
            color={colorFor(name)}
            hotkey={null}
            count={counts.get(name) ?? 0}
            active={name === activeClass}
            hidden={hiddenClasses.has(name)}
            unknown
            onPick={() => onPick(name)}
            onToggleHidden={() => onToggleHidden(name)}
            onAddToList={() => onAddToList(name)}
          />
        ))}
      </ul>
    </>
  )
}

function ClassRow({
  name,
  color,
  hotkey,
  count,
  active,
  hidden,
  unknown,
  onPick,
  onToggleHidden,
  onAddToList,
}: {
  name: string
  color: string
  hotkey: string | null
  count: number
  active: boolean
  hidden: boolean
  unknown?: boolean
  onPick: () => void
  onToggleHidden: () => void
  onAddToList?: () => void
}) {
  return (
    <li
      className={cn(
        "group flex items-center gap-1 rounded-md pr-1",
        active ? "bg-accent text-accent-foreground" : "hover:bg-muted"
      )}
    >
      <button
        type="button"
        onClick={onPick}
        aria-pressed={active}
        className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
      >
        <span
          className={cn(
            "size-3 shrink-0 rounded-sm",
            active && "ring-2 ring-ring/40 ring-offset-1 ring-offset-background"
          )}
          style={{ backgroundColor: color }}
          aria-hidden
        />
        <span className={cn("min-w-0 flex-1 truncate", hidden && "opacity-50")}>
          {name}
        </span>
        {unknown && (
          <Badge
            variant="outline"
            className="h-5 border-amber-500/50 px-1.5 text-[10px] font-normal text-amber-700 dark:text-amber-500"
            title="这个类别不在数据集的类别列表中"
          >
            不在列表
          </Badge>
        )}
        {count > 0 && (
          <span className="text-xs text-muted-foreground tabular-nums">
            {formatNumber(count)}
          </span>
        )}
        {hotkey && <Kbd>{hotkey}</Kbd>}
      </button>
      {onAddToList && (
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={onAddToList}
          aria-label={`把“${name}”加入类别列表`}
          title="加入类别列表"
        >
          <Plus />
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={onToggleHidden}
        aria-label={hidden ? `显示“${name}”的框` : `隐藏“${name}”的框`}
        aria-pressed={hidden}
        title={hidden ? "显示这个类别的框" : "隐藏这个类别的框"}
        className={cn(
          "text-muted-foreground",
          !hidden &&
            "opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
        )}
      >
        {hidden ? <EyeOff /> : <Eye />}
      </Button>
    </li>
  )
}

type ObjectPanelProps = {
  objects: EditorObject[]
  selected: string | null
  classes: DatasetClass[]
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
  classes,
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
    <Section
      title={`本图的框（${formatNumber(objects.length)}）`}
      className="min-h-0"
    >
      {objects.length === 0 ? (
        <p className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          {readOnly
            ? "没有框。"
            : "还没有框。按 W 后在图片上拖动（或先后点击两个对角）画框。"}
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
          classes={classes}
          colorFor={colorFor}
          size={size}
          readOnly={readOnly}
          onChange={(patch) => onChange(selectedObject.uid, patch)}
          onDelete={() => onDelete(selectedObject.uid)}
        />
      )}
    </Section>
  )
}

function ObjectDetails({
  object,
  classes,
  colorFor,
  size,
  readOnly,
  onChange,
  onDelete,
}: {
  object: EditorObject
  classes: DatasetClass[]
  colorFor: (name: string) => string
  size: Size | null
  readOnly: boolean
  onChange: (patch: Partial<AnnotationObject>) => void
  onDelete: () => void
}) {
  // id 不写死：页面上有多份面板（如切换布局时）也不会重复，标签对应各自的控件。
  const id = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const names = classes.map((cls) => cls.name)
  const options = names.includes(object.name) ? names : [...names, object.name]
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
            {options.map((name) => (
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
    <Section title="图片">
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
    </Section>
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

function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[4rem_minmax(0,1fr)] gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  )
}
