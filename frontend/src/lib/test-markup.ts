/**
 * 测试用：检查 renderToStaticMarkup 输出的布局类名（node 环境中没有 DOM，
 * 也无法测量尺寸）。
 */

/**
 * 找出 html 中第一个包含 text 的文本节点，返回从外到内包住它的每个元素的类名。
 * 没有找到时返回 null。html 须是 React 生成的标记：属性值中的 `<`、`>` 已转义，
 * 空元素写作 `<input/>`。
 */
export function enclosingClasses(
  html: string,
  text: string
): string[][] | null {
  const stack: string[][] = []
  const token = /<!--[\s\S]*?-->|<(\/?)([a-zA-Z][\w:-]*)([^>]*?)(\/?)>|([^<]+)/g
  for (const match of html.matchAll(token)) {
    const [, closing, tag, attributes = "", selfClosing, content] = match
    if (content !== undefined) {
      if (content.includes(text)) {
        return stack.map((classes) => [...classes])
      }
    } else if (closing) {
      stack.pop()
    } else if (tag && !selfClosing) {
      const value = /(?:^|\s)class="([^"]*)"/.exec(attributes)?.[1] ?? ""
      stack.push(value.split(/\s+/).filter(Boolean))
    }
  }
  return null
}

/**
 * 其中列宽仍是默认 auto 的 grid：auto 列的最小宽度是内容的最小宽度，
 * 一段不能断开的长文本（文件名、yaml 名、Python 对象名）会把它撑宽，
 * 内部的截断与 break-words 都不起作用。
 */
export function autoColumnGrids(classes: string[][]): string[][] {
  return classes.filter(
    (names) =>
      names.includes("grid") && !names.includes("grid-cols-[minmax(0,1fr)]")
  )
}
