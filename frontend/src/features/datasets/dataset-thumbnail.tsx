import { useState } from "react"
import { ImageOff } from "lucide-react"

import {
  fileUrl,
  thumbnailUrl,
  type DatasetImageRef,
} from "@/features/datasets/api"
import { isBrowserNativeImage } from "@/features/datasets/files"
import { cn } from "@/lib/utils"

type DatasetThumbnailProps = {
  datasetId: number
  image: DatasetImageRef
  /** 已知文件名时，只对浏览器能直接显示的格式回退到原图。 */
  fileName?: string
  size?: "sm" | "lg"
  alt?: string
  className?: string
}

/**
 * 数据集图片的缩略图。服务端无法生成缩略图（如 AVIF、损坏的文件）时回退到
 * 原图，仍然失败则显示占位图标。
 */
export function DatasetThumbnail(props: DatasetThumbnailProps) {
  // 换图（或文件变化导致版本号变化）时重置回退状态。
  return (
    <ThumbnailImage
      key={`${props.image.id}:${props.image.version}`}
      {...props}
    />
  )
}

function ThumbnailImage({
  datasetId,
  image,
  fileName,
  size = "sm",
  alt = "",
  className,
}: DatasetThumbnailProps) {
  const [stage, setStage] = useState<"thumbnail" | "original" | "failed">(
    "thumbnail"
  )

  if (stage === "failed") {
    return (
      <div
        role={alt ? "img" : undefined}
        aria-label={alt || undefined}
        className={cn(
          "flex items-center justify-center bg-muted text-muted-foreground/60",
          className
        )}
      >
        <ImageOff className="size-6" aria-hidden />
      </div>
    )
  }

  const canUseOriginal = fileName ? isBrowserNativeImage(fileName) : true
  return (
    <img
      src={
        stage === "thumbnail"
          ? thumbnailUrl(datasetId, image, size)
          : fileUrl(datasetId, image)
      }
      alt={alt}
      loading="lazy"
      decoding="async"
      draggable={false}
      onError={() =>
        setStage(
          stage === "thumbnail" && canUseOriginal ? "original" : "failed"
        )
      }
      className={cn("object-cover", className)}
    />
  )
}
