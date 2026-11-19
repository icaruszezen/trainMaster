"""Dataset, image, RFC 8878 .tar.zst, and adversarial archive fixture builders."""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import io
import os
from pathlib import Path
import struct
import tarfile
import zipfile
import zlib


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        while True:
            chunk = fh.read(1 << 16)
            if not chunk:
                break
            h.update(chunk)
    return h.hexdigest()


@dataclass(frozen=True)
class FileSnapshot:
    rel: str
    size: int
    sha256: str
    mtime_ns: int


def snapshot_tree(root: Path) -> dict[str, FileSnapshot]:
    """Capture deterministic snapshot of all regular files under root."""
    out: dict[str, FileSnapshot] = {}
    if not root.is_dir():
        return out
    for dirpath, _, filenames in os.walk(root):
        for fn in sorted(filenames):
            full = Path(dirpath) / fn
            if full.is_symlink() or not full.is_file():
                continue
            st = full.stat()
            rel = full.relative_to(root).as_posix()
            out[rel] = FileSnapshot(
                rel=rel,
                size=st.st_size,
                sha256=sha256_file(full),
                mtime_ns=st.st_mtime_ns,
            )
    return out


def make_png_bytes(
    width: int = 32,
    height: int = 24,
    rgb: tuple[int, int, int] = (64, 128, 220),
) -> bytes:
    """Create a valid 8-bit RGB PNG image using pure stdlib struct + zlib."""

    def chunk(tag: bytes, data: bytes) -> bytes:
        crc = zlib.crc32(tag + data) & 0xFFFFFFFF
        return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", crc)

    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    row = b"\x00" + bytes(rgb) * width
    raw = row * height
    idat = zlib.compress(raw, level=6)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", idat) + chunk(b"IEND", b"")


def make_voc_xml(
    *,
    width: int = 32,
    height: int = 24,
    label: str = "scratch",
    xmin: int = 2,
    ymin: int = 2,
    xmax: int = 22,
    ymax: int = 18,
) -> bytes:
    xml_text = (
        "<annotation>"
        f"<size><width>{width}</width><height>{height}</height><depth>3</depth></size>"
        f"<object><name>{label}</name><bndbox>"
        f"<xmin>{xmin}</xmin><ymin>{ymin}</ymin><xmax>{xmax}</xmax><ymax>{ymax}</ymax>"
        "</bndbox></object>"
        "</annotation>"
    )
    return xml_text.encode("utf-8")


def make_voc_dataset_zip(
    count: int = 4,
    *,
    prefix: str = "sample",
    label: str = "scratch",
    subdir: str = "",
    rgb_seed: int = 30,
) -> bytes:
    """Create a valid VOC dataset .zip archive ready for /api/v1/datasets/{id}/imports."""
    buf = io.BytesIO()
    base = f"{subdir.strip('/')}/" if subdir.strip("/") else ""
    with zipfile.ZipFile(buf, "w", compression=zipfile.ZIP_DEFLATED) as zf:
        for idx in range(1, count + 1):
            stem = f"{base}{prefix}_{idx:03d}"
            png_data = make_png_bytes(
                32,
                24,
                ((rgb_seed + idx * 29) % 255, (90 + idx * 17) % 255, (150 + idx * 11) % 255),
            )
            zf.writestr(f"{stem}.png", png_data, compress_type=zipfile.ZIP_STORED)
            zf.writestr(f"{stem}.xml", make_voc_xml(label=label))
    return buf.getvalue()


def make_yolo_dataset_zip(
    count: int = 4,
    *,
    class_name: str = "scratch",
) -> bytes:
    """Create a valid Ultralytics YOLO dataset .zip with data.yaml, images/, and labels/."""
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as zf:
        yaml_text = (
            "path: .\n"
            "train: images/train\n"
            "val: images/val\n"
            f"names:\n  0: {class_name}\n"
        )
        zf.writestr("data.yaml", yaml_text.encode("utf-8"), compress_type=zipfile.ZIP_DEFLATED)
        for idx in range(1, count + 1):
            split = "val" if idx == count and count > 1 else "train"
            png_data = make_png_bytes(32, 24, ((40 + idx * 31) % 255, 120, 200))
            zf.writestr(f"images/{split}/img_{idx:03d}.png", png_data, compress_type=zipfile.ZIP_STORED)
            zf.writestr(
                f"labels/{split}/img_{idx:03d}.txt",
                b"0 0.500000 0.500000 0.400000 0.400000\n",
                compress_type=zipfile.ZIP_DEFLATED,
            )
    return buf.getvalue()


def populate_yolo_dir(root: Path, *, count: int = 4, class_name: str = "scratch") -> None:
    """Write a complete Ultralytics YOLO tree directly into root."""
    root.mkdir(parents=True, exist_ok=True)
    raw = make_yolo_dataset_zip(count=count, class_name=class_name)
    with zipfile.ZipFile(io.BytesIO(raw), "r") as zf:
        zf.extractall(root)


# ---- RFC 8878 Zstandard Frame Crafting (Zero External Dependencies) ----


def encode_zstd_uncompressed(data: bytes) -> bytes:
    """Wrap raw bytes in a valid RFC 8878 Zstandard frame using Raw_Blocks (Block_Type=0)."""
    # Magic (4B) + Frame_Header_Descriptor (0xC0: 8-byte FCS, Window_Descriptor present) + Window_Descriptor (0x78: 8 MiB)
    out = bytearray(b"\x28\xb5\x2f\xfd\xc0\x78")
    out.extend(struct.pack("<Q", len(data)))
    max_block = 131072  # 128 KiB RFC 8878 Block_Maximum_Size
    if not data:
        out.extend((1).to_bytes(3, "little"))
        return bytes(out)
    for off in range(0, len(data), max_block):
        chunk = data[off : off + max_block]
        is_last = 1 if (off + len(chunk) >= len(data)) else 0
        block_hdr = is_last | (0 << 1) | (len(chunk) << 3)
        out.extend(block_hdr.to_bytes(3, "little"))
        out.extend(chunk)
    return bytes(out)


def make_tar_bytes(
    entries: list[tuple[str, bytes, float | None]],
    *,
    pax_format: int = tarfile.PAX_FORMAT,
) -> bytes:
    """Create a POSIX/PAX tar stream preserving microsecond mtime when provided."""
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=pax_format) as tf:
        for rel_name, content, mtime in entries:
            ti = tarfile.TarInfo(name=rel_name)
            ti.size = len(content)
            ti.mode = 0o644
            if mtime is not None:
                ti.mtime = mtime
                ti.pax_headers["mtime"] = f"{mtime:.6f}"
            tf.addfile(ti, io.BytesIO(content))
    return buf.getvalue()


def make_tar_zst(entries: list[tuple[str, bytes, float | None]]) -> bytes:
    return encode_zstd_uncompressed(make_tar_bytes(entries))


# ---- Adversarial Security Archive Crafters (Tier 2 & Tier 4) ----


def make_tarslip_tar_zst(evil_path: str = "../escaped_pwn.txt", payload: bytes = b"pwned") -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as tf:
        ti = tarfile.TarInfo(name=evil_path)
        ti.size = len(payload)
        ti.mode = 0o644
        tf.addfile(ti, io.BytesIO(payload))
    return encode_zstd_uncompressed(buf.getvalue())


def make_symlink_tar_zst(link_name: str = "evil_link", target: str = "/etc/passwd") -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as tf:
        ti = tarfile.TarInfo(name=link_name)
        ti.type = tarfile.SYMTYPE
        ti.linkname = target
        ti.mode = 0o777
        tf.addfile(ti)
    return encode_zstd_uncompressed(buf.getvalue())


def make_hardlink_tar_zst(link_name: str = "evil_hardlink", target: str = "../outside") -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as tf:
        ti = tarfile.TarInfo(name=link_name)
        ti.type = tarfile.LNKTYPE
        ti.linkname = target
        tf.addfile(ti)
    return encode_zstd_uncompressed(buf.getvalue())


def make_devnode_tar_zst(dev_name: str = "evil_char_dev") -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.PAX_FORMAT) as tf:
        ti = tarfile.TarInfo(name=dev_name)
        ti.type = tarfile.CHRTYPE
        ti.devmajor = 1
        ti.devminor = 3
        tf.addfile(ti)
    return encode_zstd_uncompressed(buf.getvalue())


def make_zipslip_zip(evil_path: str = "../../escaped_zip.txt", payload: bytes = b"zipslip") -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", compression=zipfile.ZIP_DEFLATED) as zf:
        zf.writestr("data.yaml", b"names:\n  0: scratch\n")
        zf.writestr(evil_path, payload)
    return buf.getvalue()


def make_symlink_zip(link_name: str = "sym_escape", target: str = "/etc/hosts") -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as zf:
        zf.writestr("data.yaml", b"names:\n  0: scratch\n")
        zi = zipfile.ZipInfo(link_name)
        zi.create_system = 3  # Unix
        zi.external_attr = (0o120777 << 16)  # S_IFLNK | 0777
        zf.writestr(zi, target.encode("utf-8"))
    return buf.getvalue()


def make_rle_bomb_tar_zst(uncompressed_mb: int = 8) -> bytes:
    """Craft a tiny Zstandard RLE-compressed tar archive that expands to uncompressed_mb MiB."""
    raw_file = b"\x00" * (uncompressed_mb * 1024 * 1024)
    tar_stream = make_tar_bytes([("bomb.bin", raw_file, None)])
    out = bytearray(b"\x28\xb5\x2f\xfd\xc0\x98")  # 32 MiB window
    out.extend(struct.pack("<Q", len(tar_stream)))
    max_blk = 131072
    off = 0
    n = len(tar_stream)
    while off < n:
        step = 2048 if off == 0 and n > 2048 else max_blk
        chunk = tar_stream[off : off + step]
        off += len(chunk)
        is_last = 1 if off >= n else 0
        if len(chunk) > 4 and chunk.count(chunk[0]) == len(chunk):
            # RLE_Block: Block_Type = 1, Block_Size = regenerated count, 1 byte payload!
            hdr = is_last | (1 << 1) | (len(chunk) << 3)
            out.extend(hdr.to_bytes(3, "little"))
            out.append(chunk[0])
        else:
            hdr = is_last | (0 << 1) | (len(chunk) << 3)
            out.extend(hdr.to_bytes(3, "little"))
            out.extend(chunk)
    return bytes(out)

