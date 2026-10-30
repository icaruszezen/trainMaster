import { describe, expect, it } from "vitest"

import {
  archiveFormatLabel,
  classifyPath,
  extensionOf,
  FOLDER_FILE_MAX_BYTES,
  isBrowserNativeImage,
  isJunkPath,
  pairAnnotations,
  summarizeFolder,
} from "@/features/datasets/files"

describe("classifyPath", () => {
  it("recognises image extensions case-insensitively", () => {
    for (const path of [
      "a.jpg",
      "a.JPEG",
      "dir/a.png",
      "a.bmp",
      "a.gif",
      "a.webp",
      "a.tif",
      "a.TIFF",
      "a.avif",
    ]) {
      expect(classifyPath(path), path).toBe("image")
    }
  })

  it("recognises xml annotation candidates", () => {
    expect(classifyPath("Annotations/a.xml")).toBe("annotation")
    expect(classifyPath("a.XML")).toBe("annotation")
  })

  it("ignores other files and junk", () => {
    for (const path of [
      "notes.txt",
      "labels.json",
      "README",
      ".hidden.jpg",
      "dir/._a.jpg",
      ".git/objects/a.png",
      "__MACOSX/a.jpg",
      "x/__MACOSX/y/a.xml",
      "Thumbs.db",
      "sub/DESKTOP.INI",
    ]) {
      expect(classifyPath(path), path).toBe("ignored")
    }
  })
})

describe("isJunkPath", () => {
  it("only matches whole segments", () => {
    expect(isJunkPath("a.b/c.jpg")).toBe(false)
    expect(isJunkPath("my__MACOSX/c.jpg")).toBe(false)
    expect(isJunkPath("dir/.cache/c.jpg")).toBe(true)
  })
})

describe("extensionOf", () => {
  it("returns the lower-case final extension", () => {
    expect(extensionOf("a/b.tar.GZ")).toBe(".gz")
    expect(extensionOf("a/.bashrc")).toBe("")
    expect(extensionOf("a.b/c")).toBe("")
  })
})

describe("isBrowserNativeImage", () => {
  it("excludes formats browsers cannot display", () => {
    expect(isBrowserNativeImage("a.JPG")).toBe(true)
    expect(isBrowserNativeImage("a.avif")).toBe(true)
    expect(isBrowserNativeImage("a.tif")).toBe(false)
  })
})

describe("archiveFormatLabel", () => {
  it("labels compound and simple archive formats", () => {
    expect(archiveFormatLabel("a.tar.gz")).toBe("TAR.GZ")
    expect(archiveFormatLabel("a.TGZ")).toBe("TAR.GZ")
    expect(archiveFormatLabel("a.zip")).toBe("ZIP")
    expect(archiveFormatLabel("a.7z")).toBe("7Z")
    expect(archiveFormatLabel("a.rar")).toBe("RAR")
    expect(archiveFormatLabel("a.tar.zst")).toBe("TAR.ZST")
    expect(archiveFormatLabel("a.jpg")).toBeNull()
  })
})

describe("pairAnnotations", () => {
  const pair = (images: string[], xmls: string[]) =>
    Object.fromEntries(pairAnnotations(images, xmls))

  it("pairs files with the same stem in the same directory", () => {
    expect(
      pair(["a.jpg", "sub/b.png"], ["a.xml", "sub/b.xml", "c.xml"])
    ).toEqual({ "a.jpg": "a.xml", "sub/b.png": "sub/b.xml" })
  })

  it("does not pair across directories", () => {
    expect(pair(["x/a.jpg"], ["y/a.xml", "a.xml"])).toEqual({})
  })

  it("prefers the exact stem, then falls back to case-insensitive", () => {
    expect(pair(["A.jpg"], ["a.xml", "A.xml"])).toEqual({ "A.jpg": "A.xml" })
    expect(pair(["IMG_1.JPG"], ["img_1.XML"])).toEqual({
      "IMG_1.JPG": "img_1.XML",
    })
  })

  it("supports the Pascal VOC directory layout", () => {
    expect(
      pair(
        [
          "voc/JPEGImages/0001.jpg",
          "voc/images/0002.jpg",
          "voc/other/0003.jpg",
        ],
        [
          "voc/Annotations/0001.xml",
          "voc/annotations/0002.xml",
          "voc/Annotations/0003.xml",
        ]
      )
    ).toEqual({
      "voc/JPEGImages/0001.jpg": "voc/Annotations/0001.xml",
      "voc/images/0002.jpg": "voc/annotations/0002.xml",
    })
  })

  it("prefers a same-directory xml over the VOC sibling directory", () => {
    expect(
      pair(["JPEGImages/a.jpg"], ["JPEGImages/a.xml", "Annotations/a.xml"])
    ).toEqual({ "JPEGImages/a.jpg": "JPEGImages/a.xml" })
  })

  it("gives a shared xml to the first image in path order only", () => {
    expect(pair(["a.png", "a.jpg"], ["a.xml"])).toEqual({ "a.jpg": "a.xml" })
  })

  it("handles root-level VOC directories", () => {
    expect(pair(["JPEGImages/a.jpg"], ["Annotations/a.xml"])).toEqual({
      "JPEGImages/a.jpg": "Annotations/a.xml",
    })
  })
})

describe("summarizeFolder", () => {
  const file = (path: string, size = 100) => ({ path, size })

  it("counts images, annotations, pairs and ignored files", () => {
    const summary = summarizeFolder([
      file("OK/1.jpg", 1000),
      file("OK/1.xml", 10),
      file("OK/2.jpg", 2000),
      file("NG/3.png", 3000),
      file("NG/3.xml", 30),
      file("NG/orphan.xml", 5),
      file("readme.txt", 50),
      file(".DS_Store", 1),
      file("__MACOSX/OK/._1.jpg", 1),
    ])
    expect(summary).toMatchObject({
      images: 3,
      annotations: 3,
      matched: 2,
      orphaned: 1,
      ignored: 3,
      oversized: 0,
      uploadBytes: 6045,
    })
    expect(summary.uploads.map((f) => f.path)).toEqual([
      "OK/1.jpg",
      "OK/1.xml",
      "OK/2.jpg",
      "NG/3.png",
      "NG/3.xml",
      "NG/orphan.xml",
    ])
  })

  it("skips files that are too large for a batch upload", () => {
    const summary = summarizeFolder([
      file("big.tif", FOLDER_FILE_MAX_BYTES + 1),
      file("ok.tif", FOLDER_FILE_MAX_BYTES),
    ])
    expect(summary.images).toBe(1)
    expect(summary.oversized).toBe(1)
    expect(summary.uploads).toHaveLength(1)
  })

  it("handles an empty selection", () => {
    expect(summarizeFolder([])).toMatchObject({
      images: 0,
      annotations: 0,
      matched: 0,
      uploads: [],
      uploadBytes: 0,
    })
  })
})
