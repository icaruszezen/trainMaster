/**
 * 交给浏览器原生下载：用隐藏的 `<a href download>` 打开同源的下载地址（服务端
 * 带 Content-Disposition）。文件可能有几十 GB，不能用 fetch 读进内存。
 */
export function startDownload(url: string, fileName: string) {
  const link = document.createElement("a")
  link.href = url
  link.download = fileName
  link.style.display = "none"
  document.body.append(link)
  link.click()
  link.remove()
}
