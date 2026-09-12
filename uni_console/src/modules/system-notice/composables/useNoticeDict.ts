/**
 * useNoticeDict：通知域字典对接组合式。
 *
 * 统一加载 sys_notice_{type,status,priority,publish_type,read_status} 五类字典
 * （dict store 全局缓存、懒加载、幂等），供铃铛面板 / 共享详情弹窗等渲染端
 * 取「label（列表/标签文案）」与「list_class（色彩语义）」。
 *
 * 与公告管理页（modules/system-notice/views 的 useDict 接线）共用同一数据源：
 * 后台「字典管理」改 label / list_class 后，全域同步生效。
 * labelOf 的 fallback 参数用于字典未返回（加载中/接口异常）时退回本地中文兜底文案。
 */
import { useDictStore } from '@/store/modules/dict'

export type NoticeDictCode =
  | 'sys_notice_type'
  | 'sys_notice_status'
  | 'sys_notice_priority'
  | 'sys_notice_publish_type'
  | 'sys_notice_read_status'

const CODES: NoticeDictCode[] = [
  'sys_notice_type',
  'sys_notice_status',
  'sys_notice_priority',
  'sys_notice_publish_type',
  'sys_notice_read_status'
]

export function useNoticeDict() {
  const dictStore = useDictStore()

  let loading: Promise<void> | null = null

  /** 并行加载五类字典;store 已按 code 缓存,重复调用零请求 */
  function ensure(): Promise<void> {
    if (!loading) {
      loading = Promise.all(CODES.map((code) => dictStore.load(code))).then(() => undefined)
    }
    return loading
  }

  /** value → 字典 label;缺项/未加载时用 fallback,再退化为 value 原文 */
  function labelOf(
    code: NoticeDictCode,
    value: string | number | undefined | null,
    fallback = ''
  ): string {
    if (value === undefined || value === null) return fallback
    const item = dictStore.items[code]?.find((i) => i.value === String(value))
    return (item?.label ?? fallback) || String(value)
  }

  /** value → dict list_class(primary/info/warning/danger/success),无字典时 undefined */
  function clsOf(
    code: NoticeDictCode,
    value: string | number | undefined | null
  ): string | undefined {
    if (value === undefined || value === null) return undefined
    return dictStore.items[code]?.find((i) => i.value === String(value))?.list_class
  }

  return { ensure, labelOf, clsOf }
}
