/**
 * 文件管理模块共享元数据:分类清单、扩展名 → 图标/颜色映射、格式化工具。
 *
 * 分类清单与后端 uni_core/internal/model/entity/file.go 的
 * FileCategoryExts 保持一致(新增分类需前后端同步)。
 */

import { useUserStore } from '@/store/modules/user'

export interface FileCategoryMeta {
  key: Api.File.FileCategory
  label: string
  icon: string
  /** 图标/文字颜色(tailwind text token) */
  text: string
  /** 图标底板(tailwind bg token,低透明度扁平色块) */
  tile: string
  /** 分布条/标签用纯色 */
  solid: string
}

export const FILE_CATEGORY_LIST: FileCategoryMeta[] = [
  {
    key: 'image',
    label: '图片',
    icon: 'ri:image-2-line',
    text: 'text-success',
    tile: 'bg-success/12',
    solid: 'bg-success'
  },
  {
    key: 'video',
    label: '视频',
    icon: 'ri:film-line',
    text: 'text-primary',
    tile: 'bg-primary/12',
    solid: 'bg-primary'
  },
  {
    key: 'audio',
    label: '音频',
    icon: 'ri:music-2-fill',
    text: 'text-error',
    tile: 'bg-error/12',
    solid: 'bg-error'
  },
  {
    key: 'document',
    label: '文档',
    icon: 'ri:file-text-line',
    text: 'text-info',
    tile: 'bg-info/12',
    solid: 'bg-info'
  },
  {
    key: 'archive',
    label: '压缩包',
    icon: 'ri:file-zip-line',
    text: 'text-warning',
    tile: 'bg-warning/12',
    solid: 'bg-warning'
  },
  {
    key: 'code',
    label: '代码',
    icon: 'ri:code-box-line',
    text: 'text-secondary',
    tile: 'bg-secondary/12',
    solid: 'bg-secondary'
  },
  {
    key: 'other',
    label: '其他',
    icon: 'ri:file-3-line',
    text: 'text-g-600',
    tile: 'bg-g-300/60',
    solid: 'bg-g-500'
  }
]

export const FILE_CATEGORY_MAP = Object.fromEntries(
  FILE_CATEGORY_LIST.map((item) => [item.key, item])
) as Record<Api.File.FileCategory, FileCategoryMeta>

const EXT_CATEGORY: Record<string, Api.File.FileCategory> = {
  '.jpg': 'image',
  '.jpeg': 'image',
  '.png': 'image',
  '.gif': 'image',
  '.webp': 'image',
  '.svg': 'image',
  '.bmp': 'image',
  '.ico': 'image',
  '.avif': 'image',
  '.mp4': 'video',
  '.avi': 'video',
  '.mov': 'video',
  '.mkv': 'video',
  '.webm': 'video',
  '.flv': 'video',
  '.wmv': 'video',
  '.m4v': 'video',
  '.rmvb': 'video',
  '.mp3': 'audio',
  '.wav': 'audio',
  '.flac': 'audio',
  '.aac': 'audio',
  '.ogg': 'audio',
  '.wma': 'audio',
  '.m4a': 'audio',
  '.amr': 'audio',
  '.pdf': 'document',
  '.doc': 'document',
  '.docx': 'document',
  '.xls': 'document',
  '.xlsx': 'document',
  '.ppt': 'document',
  '.pptx': 'document',
  '.txt': 'document',
  '.md': 'document',
  '.csv': 'document',
  '.rtf': 'document',
  '.odt': 'document',
  '.zip': 'archive',
  '.rar': 'archive',
  '.7z': 'archive',
  '.tar': 'archive',
  '.gz': 'archive',
  '.bz2': 'archive',
  '.xz': 'archive',
  '.js': 'code',
  '.ts': 'code',
  '.jsx': 'code',
  '.tsx': 'code',
  '.vue': 'code',
  '.py': 'code',
  '.go': 'code',
  '.java': 'code',
  '.c': 'code',
  '.cpp': 'code',
  '.h': 'code',
  '.html': 'code',
  '.css': 'code',
  '.scss': 'code',
  '.json': 'code',
  '.xml': 'code',
  '.yml': 'code',
  '.yaml': 'code',
  '.sql': 'code',
  '.sh': 'code',
  '.toml': 'code'
}

/** 细分扩展名图标(常见办公/代码格式更有辨识度) */
const EXT_ICON: Record<string, { icon: string; text: string }> = {
  '.pdf': { icon: 'ri:file-pdf-2-line', text: 'text-error' },
  '.doc': { icon: 'ri:file-word-2-line', text: 'text-info' },
  '.docx': { icon: 'ri:file-word-2-line', text: 'text-info' },
  '.xls': { icon: 'ri:file-excel-2-line', text: 'text-success' },
  '.xlsx': { icon: 'ri:file-excel-2-line', text: 'text-success' },
  '.ppt': { icon: 'ri:file-ppt-2-line', text: 'text-warning' },
  '.pptx': { icon: 'ri:file-ppt-2-line', text: 'text-warning' },
  '.md': { icon: 'ri:markdown-line', text: 'text-info' },
  '.json': { icon: 'ri:braces-line', text: 'text-success' },
  '.xml': { icon: 'ri:code-box-line', text: 'text-secondary' },
  '.html': { icon: 'ri:html5-line', text: 'text-error' },
  '.css': { icon: 'ri:css3-line', text: 'text-primary' },
  '.sql': { icon: 'ri:database-2-line', text: 'text-info' },
  '.sh': { icon: 'ri:terminal-box-line', text: 'text-success' }
}

/** 由扩展名推导分类(上传队列等无后端 category 字段的场景) */
export function categoryOfExt(ext?: string | null): Api.File.FileCategory {
  if (!ext) return 'other'
  return EXT_CATEGORY[ext.toLowerCase()] ?? 'other'
}

export interface ResolvedFileMeta extends FileCategoryMeta {
  category: Api.File.FileCategory
}

/** 汇总一个文件的图标/颜色/标签元数据(后端 category 契约类型为 string,白名单收敛到 FileCategory) */
export function fileMetaOf(file: {
  ext?: string | null
  category?: string | null
}): ResolvedFileMeta {
  const raw = file.category
  const category: Api.File.FileCategory =
    raw && raw in FILE_CATEGORY_MAP ? (raw as Api.File.FileCategory) : categoryOfExt(file.ext)
  const base = FILE_CATEGORY_MAP[category] ?? FILE_CATEGORY_MAP.other
  const special = file.ext ? EXT_ICON[file.ext.toLowerCase()] : undefined
  return {
    key: base.key,
    category,
    label: base.label,
    icon: special?.icon ?? base.icon,
    text: special?.text ?? base.text,
    tile: base.tile,
    solid: base.solid
  }
}

/** 字节数格式化:1024 进制,保留合适精度 */
export function formatBytes(bytes?: number | null): string {
  if (bytes == null || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** i
  return `${i === 0 ? value : value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[i]}`
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** 绝对时间:今年省略年份 */
export function formatTime(value?: string | Date | null): string {
  if (!value) return '-'
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime())) return '-'
  const sameYear = d.getFullYear() === new Date().getFullYear()
  const date = sameYear
    ? `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
    : `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  return `${date} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 相对时间:刚刚 / N分钟前 / N小时前 / N天前,超过 7 天回退绝对时间 */
export function formatRelativeTime(value?: string | null): string {
  if (!value) return '-'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '-'
  const diff = Date.now() - d.getTime()
  const minute = 60_000
  const hour = 60 * minute
  const day = 24 * hour
  if (diff < minute) return '刚刚'
  if (diff < hour) return `${Math.floor(diff / minute)} 分钟前`
  if (diff < day) return `${Math.floor(diff / hour)} 小时前`
  if (diff < 7 * day) return `${Math.floor(diff / day)} 天前`
  return formatTime(d)
}

export function extOf(name: string): string {
  const idx = name.lastIndexOf('.')
  return idx < 0 ? '' : name.slice(idx)
}

/** 文本类扩展名(预览时按纯文本渲染) */
export const TEXT_EXTS = [
  '.txt',
  '.md',
  '.json',
  '.log',
  '.csv',
  '.xml',
  '.yml',
  '.yaml',
  '.js',
  '.ts',
  '.jsx',
  '.tsx',
  '.vue',
  '.py',
  '.go',
  '.java',
  '.c',
  '.cpp',
  '.h',
  '.css',
  '.scss',
  '.html',
  '.sql',
  '.sh',
  '.toml',
  '.ini'
]

export function isTextFile(ext?: string | null): boolean {
  return !!ext && TEXT_EXTS.includes(ext.toLowerCase())
}

/** 触发浏览器保存 Blob 为文件 */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** 在线预览的视频/音频大小上限:超过引导下载,避免整段拉取 */
export const MAX_PREVIEW_SIZE = 80 * 1024 * 1024

/** 按钮级权限判断(与服务端权限码消费端一致,v-perm 的 h() 渲染替代) */
export function hasPermission(code: string): boolean {
  return (useUserStore().info?.permissions ?? []).includes(code)
}
