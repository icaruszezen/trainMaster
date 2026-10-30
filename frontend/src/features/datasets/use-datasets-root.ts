import { useSystemInfo } from "@/features/system/api"

/** 服务器（容器）内数据集根目录的绝对路径，如 `/data/datasets`；未知时为 null。 */
export function useDatasetsRoot(): string | null {
  const { data } = useSystemInfo()
  return (
    data?.storage.directories.find((directory) => directory.key === "datasets")
      ?.path ?? null
  )
}
