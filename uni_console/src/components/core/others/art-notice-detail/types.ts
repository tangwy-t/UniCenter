/**
 * 详情抽屉自洽契约:公告管理页传管理形态(含 createBy/status),
 * 铃铛传收件箱形态(无 createBy/status,必然已发布);可选字段条件渲染。
 */
export interface ArtNoticeDetailData {
  id: string
  title: string
  content?: string
  noticeType: number
  publishTime?: string | null
  createdAt?: string
  /** 0=普通 1=重要 2=紧急(收件箱形态渲染重要/紧急 tag) */
  priority?: number
  createBy?: string
  status?: number
}
