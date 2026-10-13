/**
 * 批量栏的**资源无关契约**：选中行的最小投影 + 一条批量动作的描述。
 *
 * 为什么行要投影而不是让批量栏直读各资源的条目类型：四个列表页（容器/镜像/数据卷/
 * 网络）的条目在协议里是四个不同的 DTO（镜像甚至没有 `name`，名字要从 repoTags
 * 推）。批量栏只关心「勾了谁、在哪台主机、受不受保护、能不能动」这四件事 ——
 * 页面把选中集投影成 BatchRow 之后，栏本身对资源一无所知（按钮标签/图标/危险度/
 * 权限码全部从 utils/actions 的注册表按动作码取）。
 *
 * `blocked` 是**页面侧的前置结论**（如「镜像正被容器使用」）：批量栏把它当作
 * 「不发送、计入失败」的原因句，和受保护目标走同一条不发送通道 —— 栏不该知道
 * 镜像在用这件事，但必须如实把页面给的结论传给用户。
 */
export interface BatchRow {
  /** 行归属主机（分波与逐行派发的主机来源）。 */
  hostId: string
  /** 行归属主机名（波级结论与确认清单里的展示；空串时回退 hostId）。 */
  hostname?: string
  /** 行名（也是逐行派发的 target —— 页面负责投影成协议认的引用）。 */
  name: string
  /** 目标是否受保护（快照里 agent 算好的结论，前端不重复判断）。 */
  protected?: boolean
  /** 页面侧的「不可执行」结论（按动作码判定）：返回原因句则该行不发送、并入失败明细。 */
  blocked?: (action: string) => string | null
}

/** 输入档（批量动作在确认弹窗里要先收集的那个参数）的描述。 */
export interface BatchInputSpec {
  label: string
  placeholder?: string
  hint?: string
  /** 必填（缺省 false）：空串不通过，提交钮禁用。 */
  required?: boolean
}

/** 一条批量动作：动作码取 utils/actions 注册表，其余形态从注册表推导。 */
export interface BatchActionSpec {
  /** 写动作码（注册表内）。 */
  action: string
  /** 逐行派发时的 options（输入档收集到的值作为第二参传入；缺省空对象）。 */
  options?: (row: BatchRow, input: string) => Record<string, unknown>
  /** 需要先收集参数的批量动作（如导出 tar 的文件名前缀）。 */
  input?: BatchInputSpec
}

/** 容器页的批量动作集（启停重启 = 一次确认档；删除 = 逐字 DELETE 档）。 */
export const CONTAINER_BATCH_ACTIONS: BatchActionSpec[] = [
  { action: 'container:start' },
  { action: 'container:stop' },
  { action: 'container:restart' },
  { action: 'container:remove' }
]

/** 镜像页的批量动作集：删除（逐字 DELETE 档）+ 导出 tar（收集文件名前缀）。 */
export const IMAGE_BATCH_ACTIONS: BatchActionSpec[] = [
  { action: 'image:remove' },
  {
    action: 'image:save',
    // 批量导出不能共用同一个文件名（同主机上必然互相覆盖），故逐行按镜像引用
    // 命名；弹窗里收集的是**前缀**（可留空），拼在自动名之前。
    input: {
      label: '文件名前缀（可选）',
      placeholder: '留空 = 按镜像引用命名',
      hint: '每行文件名 = 前缀 + 镜像引用（仓库_标签.tar），产物落在各主机的 agent 下载目录。'
    },
    options: (row, input) => ({
      filename: `${input.trim()}${imageFileStem(row.name)}.tar`
    })
  }
]

/** 数据卷页的批量动作集：删除（逐字 DELETE 档；受保护卷按保护档分流）。 */
export const VOLUME_BATCH_ACTIONS: BatchActionSpec[] = [{ action: 'volume:remove' }]

/** 网络页的批量动作集：删除（逐字 DELETE 档）。 */
export const NETWORK_BATCH_ACTIONS: BatchActionSpec[] = [{ action: 'network:remove' }]

/** 批量栏吸底块与分页器之间的间距（与 ArtTable 的 PAGINATION_SPACING 同值）。 */
export const BATCH_PAGINATION_SPACING = 6

/**
 * 批量栏吸底块要抬起多少像素，才能**不盖住分页器**。
 *
 * 吸底块（`position: sticky; bottom: X`）的约束矩形是它**自己的**最近滚动容器
 * （`overflow != visible` 的祖先）的 padding box（卡体就是），`X` 从那个矩形的下缘
 * 往上量。所以「抬到分页器上缘」= 从该容器下缘到分页器上缘的距离 + 间距 —— 直接量：
 *
 *     X = 滚动容器下缘 - 分页器上缘 + 间距
 *
 * （曾经的写法是「分页器高度 + 间距」，那只有在分页器正好贴住容器下缘时才成立；
 * 实测分页器下面还压着吸底块自身的文档流位置与卡体留白，于是抬得不够 —— 1366×768
 * 下吸底块整块盖住分页器、页码点不到。**分页器的位置才是唯一的事实来源**。）
 *
 * 入参是**吸底块自己的滚动容器**（由调用方从栏元素往上找 —— 别从分页器往上找：
 * 两者可能落在不同的滚动容器里，会量出错的值）。无分页器 / 量不出布局（jsdom 首拉）
 * 时返回 0：退回 CSS 的 `bottom: 0`。
 */
export function measureBatchOffset(scrollContainer: HTMLElement | null | undefined): number {
  if (!scrollContainer) return 0
  const pag = scrollContainer.querySelector('.pagination') as HTMLElement | null
  if (!pag) return 0
  const pagRect = pag.getBoundingClientRect()
  if (pagRect.height === 0) return 0 // 无布局（首拉/测试环境）
  const bottom = scrollContainer.getBoundingClientRect().bottom
  return Math.max(0, Math.round(bottom - pagRect.top + BATCH_PAGINATION_SPACING))
}

/**
 * 吸底块的 sticky 约束矩形所在的元素：从它自己往上找第一个滚动容器
 * （`overflow-y != visible` 的祖先）。找不到（一路到 body）时返回 null —— 调用方
 * 退回 CSS 的 `bottom: 0`。
 */
export function stickyScrollContainer(el: HTMLElement | null | undefined): HTMLElement | null {
  let sc: HTMLElement | null = el?.parentElement ?? null
  while (sc && sc !== document.body) {
    if (getComputedStyle(sc).overflowY !== 'visible') return sc
    sc = sc.parentElement
  }
  return null
}

/**
 * 镜像引用 → 文件名主干：把文件系统不认的字符（`/ : @` 等）换成下划线。
 * 与单行导出「只填文件名」的口径同源：协议侧只拦空值，文件名白名单由服务端兜，
 * 这里保证批量自动名**不含路径分隔符**（否则名字会被当成路径）。
 */
export function imageFileStem(ref: string): string {
  return ref.replace(/[^A-Za-z0-9._-]+/g, '_')
}