"""Deterministic CPU-only training subprocess obeying trainmaster_trainer TrainingProcess JSONL protocol."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import sys


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--spec", required=True)
    parser.add_argument("--audit-file", default=None)
    args = parser.parse_args()

    spec = json.loads(args.spec)
    data_yaml = Path(spec["data_yaml"])
    if not data_yaml.is_file():
        print(
            json.dumps(
                {
                    "event": "done",
                    "ok": False,
                    "error": f"data.yaml does not exist at {data_yaml}",
                }
            ),
            flush=True,
        )
        return 1

    dataset_root = data_yaml.parent
    work_dir = Path.cwd()
    zip_still_present = (work_dir / "dataset.zip").exists()

    image_files: list[str] = []
    label_files: list[str] = []
    for root, _, files in os.walk(dataset_root):
        for fn in sorted(files):
            rel = (Path(root) / fn).relative_to(dataset_root).as_posix()
            low = fn.lower()
            if low.endswith((".jpg", ".jpeg", ".png", ".webp", ".bmp")):
                image_files.append(rel)
            elif low.endswith(".txt") and rel != "classes.txt":
                label_files.append(rel)

    audit_path = args.audit_file or os.environ.get("TM_E2E_TRAINER_AUDIT_FILE")
    if audit_path:
        Path(audit_path).parent.mkdir(parents=True, exist_ok=True)
        Path(audit_path).write_text(
            json.dumps(
                {
                    "data_yaml": str(data_yaml),
                    "dataset_root": str(dataset_root),
                    "zip_present_during_training": zip_still_present,
                    "image_count": len(image_files),
                    "label_count": len(label_files),
                    "images": image_files,
                },
                indent=2,
            ),
            encoding="utf-8",
        )

    epochs = int(spec.get("params", {}).get("epochs", 1) or 1)
    save_dir = Path(spec["project_dir"]) / spec["run_name"]
    weights_dir = save_dir / "weights"
    weights_dir.mkdir(parents=True, exist_ok=True)

    # Emit start & epoch telemetry
    sys.stderr.write(f"[fake-yolo] training on {len(image_files)} images from {dataset_root}\n")
    sys.stderr.flush()
    print(json.dumps({"event": "start", "total_epochs": epochs}), flush=True)
    for ep in range(1, epochs + 1):
        print(
            json.dumps(
                {
                    "event": "epoch",
                    "epoch": {
                        "epoch": ep,
                        "total_epochs": epochs,
                        "box_loss": round(0.45 / ep, 4),
                        "cls_loss": round(0.35 / ep, 4),
                        "dfl_loss": round(0.25 / ep, 4),
                        "precision": 0.91,
                        "recall": 0.87,
                        "map50": 0.89,
                        "map50_95": 0.68,
                    },
                }
            ),
            flush=True,
        )

    # Produce deterministic YOLO weight & metric artifacts
    (weights_dir / "best.pt").write_bytes(b"PT_WEIGHTS_BEST_" + str(len(image_files)).encode("ascii") * 64)
    (weights_dir / "last.pt").write_bytes(b"PT_WEIGHTS_LAST_" + str(len(image_files)).encode("ascii") * 64)
    (save_dir / "results.csv").write_text(
        "epoch,train/box_loss,metrics/mAP50(B)\n1,0.45,0.89\n",
        encoding="utf-8",
    )

    print(
        json.dumps(
            {
                "event": "done",
                "ok": True,
                "save_dir": str(save_dir),
                "result": {
                    "best_epoch": epochs,
                    "epochs_run": epochs,
                    "precision": 0.91,
                    "recall": 0.87,
                    "map50": 0.89,
                    "map50_95": 0.68,
                },
            }
        ),
        flush=True,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
