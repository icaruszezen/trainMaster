/** 测试用的数据（只在 *.test.ts(x) 中导入）。 */
import type { Dataset, DatasetVersion } from "@/features/datasets/api"
import type {
  DataVersion,
  DataVersionPreview,
  DataVersionSkipped,
  DataVersionStats,
} from "@/features/data-versions/api"

export const NO_SKIPPED: DataVersionSkipped = {
  unannotated: 0,
  invalid: 0,
  unlisted: 0,
  empty: 0,
  with_objects: 0,
  missing: 0,
  changed: 0,
  unknown_size: 0,
  unsupported_format: 0,
  augment_skipped: 0,
}

export const EMPTY_STATS: DataVersionStats = {
  labels: [],
  negatives: { train: 0, val: 0, test: 0 },
  by_folder: 0,
  skipped: NO_SKIPPED,
  dropped_objects: 0,
  degenerate_objects: 0,
  storage: {
    linked: 0,
    copied: 0,
    generated: 0,
    orientation_fixed: 0,
    copied_bytes: 0,
    generated_bytes: 0,
  },
  compare: null,
}

export function dataVersion(changes: Partial<DataVersion> = {}): DataVersion {
  return {
    id: 12,
    project_id: 3,
    project_name: "外观检测",
    version: 4,
    note: "补充夜班数据",
    status: "ready",
    error: "",
    progress: null,
    queue_position: null,
    classes: [
      { name: "scratch", color: "#ef4444" },
      { name: "dent", color: "" },
    ],
    class_version: "c1",
    config: {
      split: {
        train: 80,
        val: 20,
        test: 0,
        seed: 42,
        use_folders: true,
        group_by: "stem",
      },
      scope: "annotated",
      include_empty: true,
      ignore_labels: [],
      augmentation: null,
    },
    sources: [
      {
        dataset_id: 1,
        dataset_name: "产线A-外观",
        dir_name: "line_a",
        include_base: true,
        versions: [{ id: 5, name: "审图1.2" }],
        scope: "annotated",
        image_count: 120,
        dataset_deleted: false,
      },
    ],
    image_count: 1000,
    object_count: 2000,
    train_count: 800,
    val_count: 200,
    test_count: 0,
    augmented_count: 0,
    total_bytes: 123_456_789,
    stats: EMPTY_STATS,
    created_at: "2026-11-01T08:00:00Z",
    updated_at: "2026-11-01T08:05:00Z",
    started_at: "2026-11-01T08:00:01Z",
    finished_at: "2026-11-01T08:05:00Z",
    ...changes,
  }
}

export function dataset(changes: Partial<Dataset> = {}): Dataset {
  return {
    id: 1,
    name: "产线A-外观",
    description: "",
    dir_name: "line_a",
    path: "/data/datasets/line_a",
    origin: "directory",
    status: "",
    status_updated_at: null,
    directory_missing: false,
    image_count: 130,
    annotated_count: 100,
    empty_count: 10,
    invalid_count: 0,
    object_count: 300,
    label_count: 2,
    total_bytes: 1_000_000,
    covers: [],
    projects: [{ id: 3, name: "外观检测" }],
    version_count: 1,
    active_import: null,
    scanned_at: null,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...changes,
  }
}

export function reviewVersion(
  changes: Partial<DatasetVersion> = {}
): DatasetVersion {
  return {
    id: 5,
    dataset_id: 1,
    kind: "review",
    name: "审图1.2",
    major: 1,
    minor: 2,
    note: "",
    status: "ready",
    image_count: 40,
    needs_sample_count: 35,
    approved_count: 5,
    object_count: 80,
    review: null,
    merge_id: 9,
    source: {
      review_id: 2,
      review_name: "第二批",
      merge_id: 9,
      statuses: ["needs_sample"],
      model: {
        id: 1,
        project_id: 3,
        project_name: "外观检测",
        version: 2,
        yolo_version: "yolo11",
        task: "detect",
        file_name: "best.pt",
      },
    },
    created_at: "2026-10-20T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
    finished_at: "2026-10-20T00:01:00Z",
    ...changes,
  }
}

export function preview(
  changes: Partial<DataVersionPreview> = {}
): DataVersionPreview {
  return {
    classes: [{ name: "scratch", color: "#ef4444" }],
    class_version: "c1",
    image_count: 1000,
    object_count: 2000,
    augmented_count: 0,
    sources: [],
    skipped: NO_SKIPPED,
    dropped_objects: 0,
    degenerate_objects: 0,
    splits: {
      train: { images: 800, objects: 1600, negatives: 10, by_folder: 0 },
      val: { images: 200, objects: 400, negatives: 2, by_folder: 0 },
      test: { images: 0, objects: 0, negatives: 0, by_folder: 0 },
    },
    labels: [
      {
        name: "scratch",
        color: "#ef4444",
        train: { objects: 1600, images: 700 },
        val: { objects: 400, images: 180 },
        test: { objects: 0, images: 0 },
      },
    ],
    unlisted_labels: [],
    compare: null,
    ...changes,
  }
}
