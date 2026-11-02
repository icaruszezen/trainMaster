import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Kbd } from "@/components/ui/kbd"
import { focusAfterDialog, shortcutHelp } from "@/features/annotation/shortcuts"

export function ShortcutsDialog({
  open,
  onOpenChange,
  mac,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  mac: boolean
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-h-[min(90dvh,820px)] overflow-y-auto sm:max-w-3xl"
        onCloseAutoFocus={(event) => focusAfterDialog(event)}
      >
        <DialogHeader>
          <DialogTitle>快捷键</DialogTitle>
          <DialogDescription>
            快捷键沿用 LabelImg 的习惯（W 画框、A / D
            切换图片），在输入框中输入时不生效。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-x-8 gap-y-6 md:grid-cols-2">
          {shortcutHelp(mac).map((group) => (
            <section key={group.title} className="grid content-start gap-2">
              <h3 className="text-xs font-medium text-muted-foreground">
                {group.title}
              </h3>
              <dl className="grid gap-1.5">
                {group.items.map((item) => (
                  <div
                    key={item.label}
                    className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)] items-start gap-3 text-sm"
                  >
                    <dt className="flex flex-wrap gap-1">
                      {item.keys.map((key) => (
                        <Kbd key={key}>{key}</Kbd>
                      ))}
                    </dt>
                    <dd className="text-muted-foreground">{item.label}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
