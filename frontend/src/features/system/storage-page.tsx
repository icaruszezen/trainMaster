import { HardDrive } from "lucide-react"

import { ModulePlaceholder } from "@/components/module-placeholder"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { useSystemInfo } from "@/features/system/api"
import {
  StorageUsage,
  SystemInfoError,
} from "@/features/system/system-status-card"

/** 与后端 storage 包中的目录布局对应。 */
const directoryPurposes: Record<string, string> = {
  images: "原始图片",
  datasets: "数据集版本与导出的训练集",
  models: "模型权重",
  runs: "训练 / 评估运行产物",
  exports: "打包导出与报告",
  tmp: "上传等临时文件",
}

export function StoragePage() {
  return (
    <ModulePlaceholder moduleId="storage">
      <StorageDirectoriesCard />
    </ModulePlaceholder>
  )
}

function StorageDirectoriesCard() {
  const { data, error, isPending, isFetching, refetch } = useSystemInfo()

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <HardDrive className="size-4 text-muted-foreground" />
          存储目录
        </CardTitle>
        <CardDescription>
          图片等文件数据以普通文件直接保存在本地目录
          {data && (
            <>
              。容器内根目录{" "}
              <code className="font-mono text-foreground">
                {data.storage.root}
              </code>
              ，对应宿主机上 <code className="font-mono">.env</code> 中{" "}
              <code className="font-mono">TM_HOST_STORAGE_DIR</code>{" "}
              指定的目录（默认为部署目录下的{" "}
              <code className="font-mono">./data/storage</code>）
            </>
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6">
        {isPending ? (
          <div className="grid gap-3">
            <Skeleton className="h-2 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : error ? (
          <SystemInfoError
            message={error.message}
            retrying={isFetching}
            onRetry={() => refetch()}
          />
        ) : (
          <>
            <StorageUsage storage={data.storage} />
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>目录</TableHead>
                  <TableHead className="hidden sm:table-cell">用途</TableHead>
                  <TableHead>容器内路径</TableHead>
                  <TableHead className="text-right">状态</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.storage.directories.map((directory) => (
                  <TableRow key={directory.key}>
                    <TableCell className="font-medium">
                      {directory.key}
                    </TableCell>
                    <TableCell className="hidden text-muted-foreground sm:table-cell">
                      {directoryPurposes[directory.key] ?? "—"}
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {directory.path}
                    </TableCell>
                    <TableCell className="text-right">
                      {directory.writable ? (
                        <Badge variant="outline">可写</Badge>
                      ) : (
                        <Badge variant="destructive">不可写</Badge>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </>
        )}
      </CardContent>
    </Card>
  )
}
