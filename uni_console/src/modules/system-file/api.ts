import request from '@/utils/http'
import type { PageResponse } from '@/types/common/response'

const PREFIX = import.meta.env.VITE_API_PREFIX

/** 分页查询文件列表 */
export function fetchFiles(params: Api.File.FileQuery) {
  return request.get<PageResponse<Api.File.FileItem>>({ url: `${PREFIX}/files`, params })
}

/** 文件概览统计(总数/总大小/近7天新增/分类计数) */
export function fetchFileStats() {
  return request.get<Api.File.FileStats>({ url: `${PREFIX}/files/stats` })
}

/**
 * 上传单个文件(multipart)。
 * - 关闭超时:大文件上传不受 15s 默认超时约束
 * - onUploadProgress 回调 0-100 进度
 * - signal 支持取消
 */
export function uploadFile(
  file: File,
  onProgress?: (percent: number) => void,
  signal?: AbortSignal
) {
  const form = new FormData()
  form.append('files', file, file.name)
  return request.post<Api.File.UploadResp>({
    url: `${PREFIX}/files`,
    data: form,
    timeout: 0,
    signal,
    onUploadProgress: (e) => {
      if (onProgress && e.total) {
        onProgress(Math.min(100, Math.round((e.loaded / e.total) * 100)))
      }
    }
  })
}

/** 重命名文件 */
export function renameFile(id: string, name: string) {
  return request.put<void>({ url: `${PREFIX}/files/${id}`, data: { name } })
}

/** 批量删除文件 */
export function deleteFiles(ids: string[]) {
  return request.del<void>({ url: `${PREFIX}/files`, data: { ids } })
}

/**
 * 拉取图片缩略图(服务端按需生成最长边 width 的 JPEG,默认 256)。
 * 仅对图片分类调用;失败时调用方回退为分类图标。
 */
export function fetchFileThumb(id: string, width = 256) {
  return request.download({ url: `${PREFIX}/files/${id}/thumbnail?width=${width}` })
}

/**
 * 拉取文件二进制内容(携带鉴权头,返回 Blob)。
 * @param asAttachment true = Content-Disposition: attachment(下载);false = inline(预览)
 */
export function fetchFileContent(id: string, asAttachment = false) {
  return request.download({
    url: `${PREFIX}/files/${id}/${asAttachment ? 'download' : 'preview'}`,
    timeout: 0
  })
}
