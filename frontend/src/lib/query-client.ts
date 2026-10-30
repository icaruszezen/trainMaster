import { QueryClient } from "@tanstack/react-query"

import { isPermanentError } from "@/lib/api"

/**
 * 全局唯一的 QueryClient。除了 React 组件，上传管理器等模块级代码也通过它
 * 读取和刷新服务端状态（例如导入完成后刷新数据集列表）。
 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // 网络错误、网关 5xx 可能是暂时的，重试一次；4xx / 501 重试也没用。
      retry: (failureCount, error) =>
        failureCount < 1 && !isPermanentError(error),
      staleTime: 10_000,
    },
  },
})
