# Test fixtures

Small archives for `archive_test.go` and JPEG images for `thumbnail_test.go`, in formats Go cannot write itself.

| File | Origin |
|---|---|
| `sample.7z` | Generated with py7zr 1.1.4: `set/sub/a.png` (8×6 PNG), `set/sub/a.xml` (VOC, one `scratch` object), `set/readme.txt` |
| `encrypted.7z` | Generated with py7zr, password `secret`, file list readable, contents AES-encrypted |
| `encrypted-header.7z` | Generated with py7zr, password `secret`, header encryption on |
| `progressive-420.jpg`, `progressive-gray.jpg`, `cmyk.jpg` | Generated with Pillow 12.3: a 640×480 gradient saved as progressive 4:2:0, progressive grayscale and baseline CMYK (Adobe APP14) JPEG |
| `sample.tar.bz2` | Generated with Python's `tarfile`: `set/a.png`, `set/a.xml` (same content as in `sample.7z`) |
| `rar3-subdirs.rar`, `rar5-subdirs.rar`, `rar5-solid.rar`, `rar5-symlink-unix.rar`, `rar5-psw.rar`, `rar5-hpsw.rar`, `rar3-comment-psw.rar` | Unmodified from the test suite of [markokr/rarfile](https://github.com/markokr/rarfile/tree/master/test/files) |
| `rar3-vols.part1-head.rar`, `rar5-vols.part1-head.rar` | First 1024 bytes of `rar3-vols.part1.rar` / `rar5-vols.part1.rar` from markokr/rarfile (the tests only read the archive headers) |

The rarfile files are distributed under the ISC license:

```text
Copyright (c) 2005-2026 Marko Kreen <markokr@gmail.com>

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```
