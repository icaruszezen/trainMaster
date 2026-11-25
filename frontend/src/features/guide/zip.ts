export type ZipEntry = {
  /** ZIP 内部相对路径，使用正斜杠分隔（例如 `word/document.xml`）。 */
  name: string
  data: Uint8Array
  modifiedAt?: Date
}

const textEncoder = new TextEncoder()

/** 将 UTF-8 字符串编码为字节数组。 */
export function encodeUtf8(text: string): Uint8Array {
  return textEncoder.encode(text)
}

const CRC32_TABLE = (() => {
  const table = new Uint32Array(256)
  for (let i = 0; i < 256; i++) {
    let c = i
    for (let k = 0; k < 8; k++) {
      c = (c & 1) !== 0 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
    }
    table[i] = c >>> 0
  }
  return table
})()

/** 计算标准 IEEE 802.3 CRC-32 校验值（无符号 32 位整数）。 */
export function crc32(bytes: Uint8Array): number {
  let crc = 0xffffffff
  for (let i = 0; i < bytes.length; i++) {
    crc = CRC32_TABLE[(crc ^ bytes[i]) & 0xff] ^ (crc >>> 8)
  }
  return (crc ^ 0xffffffff) >>> 0
}

function toDosDateTime(date: Date): { dosTime: number; dosDate: number } {
  const year = Math.min(2107, Math.max(1980, date.getFullYear()))
  const month = date.getMonth() + 1
  const day = date.getDate()
  const hours = date.getHours()
  const minutes = date.getMinutes()
  const seconds = Math.floor(date.getSeconds() / 2)

  const dosTime =
    ((hours & 0x1f) << 11) | ((minutes & 0x3f) << 5) | (seconds & 0x1f)
  const dosDate =
    (((year - 1980) & 0x7f) << 9) | ((month & 0x0f) << 5) | (day & 0x1f)
  return { dosTime, dosDate }
}

/**
 * 构造符合 PKZIP 规范的无压缩（STORE）归档字节流，用于客户端零依赖生成 `.docx` 等
 * OOXML 文档包。开启 UTF-8 文件名标志位（0x0800），兼容 Microsoft Word、WPS、
 * LibreOffice 与常见解压工具。
 */
export function buildZipArchive(entries: ZipEntry[]): Uint8Array {
  const prepared = entries.map((entry) => {
    const normalizedName = entry.name.replace(/^\/+/, "")
    const nameBytes = encodeUtf8(normalizedName)
    const { dosTime, dosDate } = toDosDateTime(entry.modifiedAt ?? new Date())
    const checksum = crc32(entry.data)
    return {
      nameBytes,
      data: entry.data,
      dosTime,
      dosDate,
      checksum,
      localHeaderOffset: 0,
    }
  })

  let localSectionSize = 0
  let centralDirSize = 0
  for (const item of prepared) {
    item.localHeaderOffset = localSectionSize
    localSectionSize += 30 + item.nameBytes.length + item.data.length
    centralDirSize += 46 + item.nameBytes.length
  }

  const totalSize = localSectionSize + centralDirSize + 22
  const out = new Uint8Array(totalSize)
  const view = new DataView(out.buffer)

  let offset = 0

  // 1. Local File Headers + Data
  for (const item of prepared) {
    view.setUint32(offset, 0x04034b50, true) // Local file header signature
    view.setUint16(offset + 4, 20, true) // Version needed to extract (2.0)
    view.setUint16(offset + 6, 0x0800, true) // UTF-8 filename flag
    view.setUint16(offset + 8, 0, true) // Compression method: STORE (0)
    view.setUint16(offset + 10, item.dosTime, true)
    view.setUint16(offset + 12, item.dosDate, true)
    view.setUint32(offset + 14, item.checksum, true)
    view.setUint32(offset + 18, item.data.length, true) // Compressed size
    view.setUint32(offset + 22, item.data.length, true) // Uncompressed size
    view.setUint16(offset + 26, item.nameBytes.length, true)
    view.setUint16(offset + 28, 0, true) // Extra field length
    offset += 30

    out.set(item.nameBytes, offset)
    offset += item.nameBytes.length

    out.set(item.data, offset)
    offset += item.data.length
  }

  const centralDirOffset = offset

  // 2. Central Directory File Headers
  for (const item of prepared) {
    view.setUint32(offset, 0x02014b50, true) // Central directory header signature
    view.setUint16(offset + 4, 20, true) // Version made by
    view.setUint16(offset + 6, 20, true) // Version needed to extract
    view.setUint16(offset + 8, 0x0800, true) // UTF-8 filename flag
    view.setUint16(offset + 10, 0, true) // Compression method: STORE (0)
    view.setUint16(offset + 12, item.dosTime, true)
    view.setUint16(offset + 14, item.dosDate, true)
    view.setUint32(offset + 16, item.checksum, true)
    view.setUint32(offset + 20, item.data.length, true)
    view.setUint32(offset + 24, item.data.length, true)
    view.setUint16(offset + 28, item.nameBytes.length, true)
    view.setUint16(offset + 30, 0, true) // Extra field length
    view.setUint16(offset + 32, 0, true) // File comment length
    view.setUint16(offset + 34, 0, true) // Disk number start
    view.setUint16(offset + 36, 0, true) // Internal file attributes
    view.setUint32(offset + 38, 0, true) // External file attributes
    view.setUint32(offset + 42, item.localHeaderOffset, true)
    offset += 46

    out.set(item.nameBytes, offset)
    offset += item.nameBytes.length
  }

  // 3. End of Central Directory Record (EOCD)
  view.setUint32(offset, 0x06054b50, true) // EOCD signature
  view.setUint16(offset + 4, 0, true) // Number of this disk
  view.setUint16(offset + 6, 0, true) // Disk where central directory starts
  view.setUint16(offset + 8, prepared.length, true) // Entries on this disk
  view.setUint16(offset + 10, prepared.length, true) // Total entries
  view.setUint32(offset + 12, centralDirSize, true)
  view.setUint32(offset + 16, centralDirOffset, true)
  view.setUint16(offset + 20, 0, true) // ZIP comment length

  return out
}
