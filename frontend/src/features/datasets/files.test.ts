import { describe, expect, it } from "vitest"

import {
  archiveFormatLabel,
  classifyPath,
  extensionOf,
  findClassesFile,
  FOLDER_FILE_MAX_BYTES,
  isBrowserNativeImage,
  isJunkPath,
  pairAnnotations,
  suggestAnnotationFormat,
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

describe("classifyPath with an annotation format", () => {
  it("only accepts the selected format's annotation files", () => {
    expect(classifyPath("a.xml", "voc")).toBe("annotation")
    expect(classifyPath("a.json", "voc")).toBe("ignored")
    expect(classifyPath("a.txt", "voc")).toBe("ignored")

    expect(classifyPath("a.json", "xanylabeling")).toBe("annotation")
    expect(classifyPath("sub/a.JSON", "xanylabeling")).toBe("annotation")
    expect(classifyPath("a.xml", "xanylabeling")).toBe("ignored")
    expect(classifyPath("a.txt", "xanylabeling")).toBe("ignored")

    expect(classifyPath("labels/a.txt", "yolo")).toBe("annotation")
    expect(classifyPath("a.TXT", "yolo")).toBe("annotation")
    expect(classifyPath("a.xml", "yolo")).toBe("ignored")
    expect(classifyPath("a.json", "yolo")).toBe("ignored")
  })

  it("never treats classes.txt as a YOLO label file", () => {
    expect(classifyPath("classes.txt", "yolo")).toBe("ignored")
    expect(classifyPath("labels/Classes.TXT", "yolo")).toBe("ignored")
    expect(classifyPath("my_classes.txt", "yolo")).toBe("annotation")
  })

  it("keeps images and junk rules for every format", () => {
    for (const format of ["voc", "xanylabeling", "yolo"] as const) {
      expect(classifyPath("a.PNG", format)).toBe("image")
      expect(classifyPath("__MACOSX/a.txt", format)).toBe("ignored")
      expect(classifyPath(".hidden/a.json", format)).toBe("ignored")
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

describe("pairAnnotations for X-AnyLabeling", () => {
  const pair = (images: string[], jsons: string[]) =>
    Object.fromEntries(pairAnnotations(images, jsons, "xanylabeling"))

  it("pairs a same-stem json in the same directory", () => {
    expect(
      pair(["a.jpg", "sub/b.png", "c.jpg"], ["a.json", "sub/b.json", "x.json"])
    ).toEqual({ "a.jpg": "a.json", "sub/b.png": "sub/b.json" })
  })

  it("prefers the exact stem, then falls back to case-insensitive", () => {
    expect(pair(["A.jpg"], ["a.json", "A.json"])).toEqual({
      "A.jpg": "A.json",
    })
    expect(pair(["IMG_1.JPG"], ["img_1.JSON"])).toEqual({
      "IMG_1.JPG": "img_1.JSON",
    })
  })

  it("has no directory-layout rule", () => {
    expect(
      pair(
        ["JPEGImages/a.jpg", "images/b.jpg"],
        ["Annotations/a.json", "labels/b.json"]
      )
    ).toEqual({})
  })

  it("gives a shared json to the first image in path order only", () => {
    expect(pair(["a.png", "a.jpg"], ["a.json"])).toEqual({ "a.jpg": "a.json" })
  })
})

describe("pairAnnotations for YOLO", () => {
  const pair = (images: string[], txts: string[]) =>
    Object.fromEntries(pairAnnotations(images, txts, "yolo"))

  it("pairs a same-stem txt in the same directory", () => {
    expect(pair(["a.jpg", "sub/b.png"], ["a.txt", "sub/b.txt"])).toEqual({
      "a.jpg": "a.txt",
      "sub/b.png": "sub/b.txt",
    })
    expect(pair(["IMG_1.JPG"], ["img_1.TXT"])).toEqual({
      "IMG_1.JPG": "img_1.TXT",
    })
  })

  it("supports the Ultralytics images/ ↔ labels/ layout", () => {
    expect(
      pair(
        ["images/train/a.jpg", "ds/images/b.png", "images/c.jpg"],
        ["labels/train/a.txt", "ds/labels/b.txt", "labels/c.txt"]
      )
    ).toEqual({
      "images/train/a.jpg": "labels/train/a.txt",
      "ds/images/b.png": "ds/labels/b.txt",
      "images/c.jpg": "labels/c.txt",
    })
  })

  it("matches the labels path case-insensitively", () => {
    expect(pair(["Images/Train/IMG.jpg"], ["LABELS/train/img.txt"])).toEqual({
      "Images/Train/IMG.jpg": "LABELS/train/img.txt",
    })
  })

  it("replaces only the last images segment", () => {
    expect(
      pair(
        ["images/set/images/a.jpg"],
        ["labels/set/images/a.txt", "images/set/labels/a.txt"]
      )
    ).toEqual({ "images/set/images/a.jpg": "images/set/labels/a.txt" })
  })

  it("prefers a same-directory txt over the labels directory", () => {
    expect(pair(["images/a.jpg"], ["images/a.txt", "labels/a.txt"])).toEqual({
      "images/a.jpg": "images/a.txt",
    })
  })

  it("does not look in labels/ for images outside an images directory", () => {
    expect(
      pair(["train/a.jpg", "a.jpg"], ["labels/a.txt", "train/labels/a.txt"])
    ).toEqual({})
  })

  it("gives a shared txt to the first image in path order only", () => {
    expect(pair(["images/a.png", "images/a.jpg"], ["labels/a.txt"])).toEqual({
      "images/a.jpg": "labels/a.txt",
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

  it("keeps VOC as the default format", () => {
    const files = [file("a.jpg"), file("a.xml"), file("a.txt"), file("b.json")]
    expect(summarizeFolder(files)).toEqual(summarizeFolder(files, "voc"))
    expect(summarizeFolder(files)).toMatchObject({
      format: "voc",
      images: 1,
      annotations: 1,
      matched: 1,
      ignored: 2,
    })
  })

  it("uploads only YOLO labels when YOLO is selected", () => {
    const summary = summarizeFolder(
      [
        file("classes.txt", 20),
        file("images/train/1.jpg", 1000),
        file("images/train/2.jpg", 1000),
        file("images/train/2.xml", 30),
        file("labels/train/1.txt", 10),
        file("labels/train/orphan.txt", 10),
        file("labels/Classes.txt", 20),
        file("data.yaml", 40),
      ],
      "yolo"
    )
    expect(summary).toMatchObject({
      format: "yolo",
      images: 2,
      annotations: 2,
      matched: 1,
      orphaned: 1,
      ignored: 4,
      uploadBytes: 2020,
    })
    expect(summary.uploads.map((f) => f.path)).toEqual([
      "images/train/1.jpg",
      "images/train/2.jpg",
      "labels/train/1.txt",
      "labels/train/orphan.txt",
    ])
  })

  it("uploads only json files when X-AnyLabeling is selected", () => {
    const summary = summarizeFolder(
      [file("a.jpg"), file("a.json"), file("a.xml"), file("b.png")],
      "xanylabeling"
    )
    expect(summary).toMatchObject({
      format: "xanylabeling",
      images: 2,
      annotations: 1,
      matched: 1,
      ignored: 1,
    })
    expect(summary.uploads.map((f) => f.path)).toEqual([
      "a.jpg",
      "a.json",
      "b.png",
    ])
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

describe("findClassesFile", () => {
  const file = (path: string) => ({ path, size: 10 })

  it("prefers the shallowest classes.txt, case-insensitively", () => {
    expect(
      findClassesFile([
        file("a/b/classes.txt"),
        file("labels/CLASSES.TXT"),
        file("z/classes.txt"),
        file("images/a.jpg"),
      ])?.path
    ).toBe("labels/CLASSES.TXT")
    expect(findClassesFile([file("x/a.txt"), file("Classes.txt")])?.path).toBe(
      "Classes.txt"
    )
  })

  it("ignores junk files and returns null when there is none", () => {
    expect(findClassesFile([file("__MACOSX/classes.txt")])).toBeNull()
    expect(findClassesFile([file("a.txt"), file("names.txt")])).toBeNull()
  })
})

describe("suggestAnnotationFormat", () => {
  const file = (path: string) => ({ path, size: 10 })
  const suggest = (
    paths: string[],
    format: "voc" | "xanylabeling" | "yolo"
  ) => {
    const files = paths.map(file)
    return suggestAnnotationFormat(files, summarizeFolder(files, format))
  }

  it("suggests the format that pairs the most images", () => {
    expect(
      suggest(
        [
          "images/a.jpg",
          "images/b.jpg",
          "labels/a.txt",
          "labels/b.txt",
          "images/a.json",
        ],
        "voc"
      )
    ).toEqual({ format: "yolo", matched: 2 })
  })

  it("stays quiet when the selected format already pairs something", () => {
    expect(
      suggest(["a.jpg", "a.xml", "b.jpg", "b.txt", "c.jpg", "c.txt"], "voc")
    ).toBeNull()
  })

  it("stays quiet when no format pairs anything", () => {
    expect(suggest(["a.jpg", "b.jpg", "notes.txt"], "voc")).toBeNull()
    expect(suggest(["a.txt"], "voc")).toBeNull()
  })
})
