<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="docker-tasks art-full-height">
    <ElCard class="art-table-card" shadow="never">
      <!-- :layout 显式去掉 columns 段（组件默认含）：本页不是 ArtTable（任务清单
           是自绘 <table> 行表），工具条没有列集合 —— 保留列设置钮只会开出空弹层
           （死控件）；其余段与组件默认一致。 -->
      <ArtTableHeader
        :loading="loading"
        layout="search,refresh,size,fullscreen,settings"
        @refresh="manualRefresh"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- 返回上一页：本页没有菜单入口（从容器页/总览的入口进），返回键就是它的出口。 -->
            <ArtButtonTable
              icon="ri:arrow-left-line"
              icon-class="bg-g-300/55 text-g-700"
              title="返回上一页"
              @click="back"
            />
            <!-- 状态过滤（服务端过滤）：全部 / 进行中（pending）/ 已结束（一切终态）。 -->
            <ElRadioGroup v-model="filter" size="small">
              <ElRadioButton value="all">全部</ElRadioButton>
              <ElRadioButton value="pending">进行中</ElRadioButton>
              <ElRadioButton value="done">已结束</ElRadioButton>
            </ElRadioGroup>
          </div>
        </template>
      </ArtTableHeader>

      <!-- ── 首拉加载（还没有任何行可给）：等价最小 loading（骨架）——
           不再有页面级加载卡外观（列表页纯化：加载态的表意交给列表自身）。 ── -->
      <div v-if="state === 'loading'" class="tk-loading">
        <ElSkeleton :rows="6" animated />
      </div>

      <!-- ── 首拉失败：紧凑失败态 —— 结论句 + 重试（不显示旧列表 —— 那时还没有
           旧列表可显示；全局错误 toast 由 utils/http 的 request() 统一弹出）。 ── -->
      <ElEmpty v-else-if="state === 'error'" class="docker-empty" description="任务列表获取失败">
        <ElButton size="small" @click="manualRefresh">重试</ElButton>
      </ElEmpty>

      <!-- ── 空态（「没有任务」与「筛完了没有」分开说，两种事实不是一句话） ── -->
      <ElEmpty v-else-if="state === 'empty'" class="docker-empty" :description="emptyText" />

      <!-- ── 正常态：轮询失败标注 + 任务表 + 分页 ── -->
      <template v-else>
        <!-- 静默刷新失败：保留最后已知列表并说出口（与列表页「上次数据仍在」同一口径）。 -->
        <p v-if="refreshError" class="tk-refresh-error">刷新失败，正在显示上次结果</p>
        <!-- ── 任务表（列式）：与模块其余列表页同一张**表语言** —— 可见表头、单行
             行高、行分隔线与悬停、主机胶囊、等宽目标、时间列右对齐。改造前是自绘
             两行块（动作+目标一行、元信息一行），目标短时中间一大片空白、时间被
             margin-left:auto 推到极右，没有列对齐，1920 下就是一条没排版的清单。
             表头**吸顶**（.tk-list 自滚，见样式注释）：翻页/滚动时「这列是什么」
             始终在屏上。
             为什么不是 ArtTable（ElTable）：本表每行都要能带一条**跨列铺满**的展开区
             （进度流 / 终态结论句），而展开区是「这一行的第二块」而不是一个单元格；
             原生 <table> 的 <tbody> 行分组 + colspan 正好表达它。ElTable 的展开列是
             **每行都画开关**的行首列，与「没有可看的事实就不给开关」直接相左
             （events 页的自建 ElTable 先例同理：那页也不需要行级展开）。 -->
        <div class="tk-list">
          <table class="tk-table">
            <thead>
              <tr class="tk-head">
                <th class="tk-col--status" scope="col">状态</th>
                <th class="tk-col--action" scope="col">动作</th>
                <th class="tk-col--target" scope="col">目标</th>
                <th class="tk-col--host" scope="col">主机</th>
                <th class="tk-col--user" scope="col">发起人</th>
                <th class="tk-col--time is-num" scope="col">时间</th>
                <th class="tk-col--toggle" scope="col"><span class="tk-sr">展开</span></th>
              </tr>
            </thead>
            <tbody
              v-for="item in items"
              :key="item.ref"
              class="tk-item"
              :class="{ 'is-open': isExpanded(item.ref) }"
            >
              <tr class="tk-item__line">
                <!-- 状态：圆点 + 状态词**同列**（颜色永不单独传达 —— 文字本身就是结论）。
                     pending 的原点换旋转图标（8px 圆点放不下「在执行」的表意）；
                     未知终态照实给中性点 + 原文 —— 服务端加了新状态时页面不冒充它认识。 -->
                <td class="tk-col--status tk-item__status">
                  <span class="tk-status">
                    <span
                      class="tk-item__dot"
                      :class="{ 'is-pending': isPending(item.status) }"
                      :style="
                        isPending(item.status)
                          ? undefined
                          : { color: statusDotColor(taskStatusTone(item.status)) }
                      "
                    >
                      <ArtSvgIcon
                        v-if="isPending(item.status)"
                        icon="ri:loader-4-line"
                        class="tk-spin"
                      />
                    </span>
                    <span
                      class="tk-status__text"
                      :style="{ color: statusTextColor(taskStatusTone(item.status)) }"
                      >{{ taskStatusText(item.status) }}</span
                    >
                  </span>
                </td>
                <!-- 动作：注册表 label（页面只说人话）；查不到的（终端/配置编辑类，
                     本就不在二期写注册表里）原样等宽显示 —— 等宽本身就是「这是原始码」的提示。 -->
                <td
                  class="tk-col--action tk-item__action"
                  :class="{ 'is-raw': !isKnownAction(item) }"
                  :title="actionLabel(item)"
                >
                  {{ actionLabel(item) }}
                </td>
                <!-- 目标：等宽 + 截断（悬停看全名 —— 完整引用是排障钥匙，不给截断丢掉）。 -->
                <td class="tk-col--target tk-item__target" :title="item.target">
                  {{ targetText(item.target) }}
                </td>
                <!-- 主机：胶囊（主机是事实上的分组键，与容器/镜像/卷/网络/项目页同款）。
                     设备已删时回退 hostId；hostId 全值挂 title（排障引用）。 -->
                <td class="tk-col--host tk-item__host" :title="`主机 ${item.hostId}`">
                  <span class="docker-host">{{ item.hostname || item.hostId }}</span>
                </td>
                <td class="tk-col--user tk-item__user">{{ item.username || '—' }}</td>
                <!-- 时间：相对时间右对齐 + 等宽数字（受理时刻是次要事实，靠**列位置**
                     与数字对齐承载，不再用 margin-left:auto 单飞）。
                     createdAt 是毫秒，除千的浮点秒交给 eventRelativeTime 整段取整
                     （QA 路 1 B1 的浮点文案修复在函数内，惠及全部调用方）。 -->
                <td class="tk-col--time is-num tk-item__time">{{
                  eventRelativeTime(item.createdAt / 1000, nowSec)
                }}</td>
                <td class="tk-col--toggle">
                  <!-- 展开开关：「收起 ≠ 取消」（任务照常跑）的语义收在 title 上，
                       不占行内正文（展开区的可见文案只留事实）。 -->
                  <button
                    v-if="isExpandable(item) || isExpanded(item.ref)"
                    type="button"
                    class="tk-item__toggle"
                    :title="isExpanded(item.ref) ? '收起（不取消任务）' : '展开'"
                    @click="toggleExpand(item)"
                  >
                    <ArtSvgIcon
                      :icon="isExpanded(item.ref) ? 'ri:arrow-up-s-line' : 'ri:arrow-down-s-line'"
                    />
                  </button>
                </td>
              </tr>
              <!-- 展开区：**整行**（跨列铺满）的第二块，不是某一列的单元格。
                   进行中的进度族任务（pull/build/push）接实时进度流 —— 收起/离开页面只是
                   停止观看（断流不发 cancel），**再次展开即重接**（进度可续）；取消是
                   展开区里的另一个动作（见子组件）。终态行给结论句原文（失败/超时的排障
                   线索；成功收尾时同样留在原地）。**参数块**（TaskOptions）是追加的一块：
                   有 options（受理时下发的动作参数原文）就渲染，与进度流 / 结论句并存，
                   不改动它们任何一条既有行为。 -->
              <tr v-if="isExpanded(item.ref)" class="tk-item__detail">
                <td class="tk-col--detail" colspan="7">
                  <TaskProgress
                    v-if="item.status === 'pending' && isProgressAction(item)"
                    :key="item.ref"
                    :host-id="item.hostId"
                    :cmd-ref="item.ref"
                    :action="item.action"
                    :can-cancel="canCancel(item)"
                  />
                  <p v-else-if="item.summary" class="tk-item__summary">{{ item.summary }}</p>
                  <!-- 参数块（本波追加）：受理时下发的动作参数原文 —— 有 options 才渲染
                       （字段缺席 = 没有看这笔任务参数的资格，缺席即不渲染任何东西，
                       与全站「不渲染 ≠ 禁用」一致，也不写「无权限」这类解释句）。 -->
                  <TaskOptions
                    v-if="hasOptions(item)"
                    :options="item.options ?? ''"
                    :action="item.action"
                  />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <!-- 分页（8d）：条数与页码都来自服务端（合并列表的真实全量 + 本页页码），
             页面只显示这两个硬事实；无行可翻（共 0 条）时整条不出。 -->
        <div v-if="total > 0" class="tk-page">
          <span class="tk-page__hint">第 {{ page }} 页 / 共 {{ total }} 条</span>
          <ElPagination
            background
            small
            layout="sizes, prev, pager, next"
            :page-sizes="[10, 20, 50, 100]"
            :total="total"
            :current-page="page"
            :page-size="pageSize"
            @current-change="onCurrentChange"
            @size-change="onSizeChange"
          />
        </div>
      </template>
    </ElCard>
  </div>
</template>

<script setup lang="ts">
  /**
   * 任务中心页（8c 页面化，`/docker/tasks`）：长任务（拉取/启停/重建…）的跨主机收口
   * 面板 —— 「刚才受理了什么、进行到哪、结论是什么」一张列表读完。本波「列表页纯化」
   * 后 = 表卡（工具条：返回 + 状态过滤；列表/空态/分页在卡内）的纯清单形态（对齐
   * 系统管理的列表形态）；页头（hero：图标+标题+副标题+刷新钮）整块删除 —— 刷新
   * 由 ArtTableHeader 的内置钮承担，机制说明句（节拍/跨主机定位）按零解释文案纪律
   * 一并删除（条数事实在分页条「第 x 页 / 共 N 条」里）。
   *
   * 列表形态（本波美化）：**列式表**（原生 <table>：吸顶表头 + 单行行高 + 展开区
   * 跨列铺满），与模块其余列表页同一张表语言 —— 改造前是自绘两行块（动作+目标一行、
   * 元信息一行），目标短时中间一大片空白、时间被 margin-left:auto 推到极右，没有
   * 列对齐。为什么不上 ArtTable/ElTable：本表每行要能带一条跨列铺满的展开区，而
   * ElTable 的展开列是**每行都画开关**的行首列（与「没有可看的事实就不给开关」
   * 相左），且展开区是「这一行的第二块」而不是一个单元格 —— 原生表的 <tbody> 行分组
   * + colspan 正好表达它。
   *
   * 数据是**一份**（GET /docker/tasks：实时 ∪ 历史合并、受理时刻降序、跨主机、
   * 服务端分页），不与任何单主机页面状态耦合 —— 这也是入口虽然长在总览 hero 与容器页
   * 工具条上、列表却不按当前主机过滤的原因：hostname 列已经回答「在哪台机器上」，
   * 再按入口主机收窄会把「跨主机收口」这个立身之本又交回去。
   *
   * 分页（8d）：**服务端分页**（page/pageSize 透传 + total 回显），前端不本地切
   * —— 历史面让列表不再是一个「最近 100 条」的易失窗口（旧上限已改写为分页事实），
   * 分页条「第 x 页 / 共 N 条」两处读的是同一个 total。越界页（保留清理后页码落空）
   * 由 load 内的自愈拉回最后一页，见那里的注释。
   *
   * 列表**只含变更/长任务动作**：只读动作（inspect 档的日志/统计/详情/配置读取 ——
   * 页面浏览就会被自动受理）在服务端任务面就被剔除（docker_tasks.go 的读面收口，
   * QA 路 1 P2 的「被只读指令刷屏」修复），本页的动作原文兜底因此只剩终端/配置
   * 编辑这类不在写注册表里的动作。
   *
   * 刷新纪律（对齐列表页自动刷新）：进页面即拉、可见期间 5 秒一轮（后台标签页跳过
   * tick）、手动刷新随时可插队（seq 守卫防旧响应晚到覆盖新数据）；keep-alive 失活/
   * 卸载停表，重新激活回来补拉。**进度流不在此列**：展开中的进度流在页面失活时
   * 保持连接，收起/卸载只是停止观看（断流不发 cancel，任务照常跑完 —— 后端
   * docker_stream.go 的 servePull/Build/PushNDJSON 收尾纪律），再次展开即重接
   *（进度可续，详见 task-progress 的生命周期取舍）；停表停的是这份列表的轮询开销。
   *
   * 过滤是**就地状态**（不进 query，原样平移抽屉口径）：服务端过滤每次切换即重拉，
   * 进 query 会让后退键变成「筛选项切换器」，与「回上一页」的直觉打架。
   *
   * 本页没有后端菜单种子（裁定：不加菜单，从总览 hero 与容器页入口进）—— 路由
   * 取 isHide 形态，理由见 index.ts 的注释（可见且无菜单的路由根本注册不上）。
   */
  import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import {
    ElButton,
    ElCard,
    ElEmpty,
    ElPagination,
    ElRadioButton,
    ElRadioGroup,
    ElSkeleton
  } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import { useAuth } from '@/hooks/core/useAuth'
  import { useUserStore } from '@/store/modules/user'
  import { PermDockerManage } from '@/enums/permission'
  import { fetchDockerTasks, type DockerTaskItem } from '../api'
  import TaskProgress from '../components/task-progress.vue'
  import TaskOptions from '../components/task-options.vue'
  import { lookupDockerAction } from '../utils/actions'
  import { eventRelativeTime } from '../utils/events'
  import { statusDotColor, statusTextColor, taskStatusText, taskStatusTone } from '../utils/status'

  defineOptions({ name: 'DockerTasksPage' })

  const router = useRouter()

  /** 返回上一页：本页没有菜单入口（从各页入口进），返回键就是它的出口。 */
  function back() {
    router.back()
  }

  // ── 数据与四态（首拉 loading/error、空、ready；静默刷新失败保留旧列表） ──

  const items = ref<DockerTaskItem[]>([])
  /** 合计条数：服务端合并列表（实时 ∪ 历史）的真实全量 —— 分页条「第 x 页 / 共 N 条」
      读它（8d 起不再是「窗口内匹配数」，理由见 api.ts 的 DockerTasksQuery 注释）。 */
  const total = ref(0)
  /** 页码与页大小（服务端分页：切页/切大小即重拉，前端不本地切）。 */
  const page = ref(1)
  // 默认 20：与各列表页的 50 档同向（10 一屏在 1440 高的大屏上只铺半屏就翻页，
  // 而任务中心的阅读形态是「扫一眼最近发生了什么」）。可选档仍含 10/20/50/100。
  const pageSize = ref(20)
  const loading = ref(false)
  /** 首拉失败（还没有任何行可给）：错误态 + 重试。 */
  const firstError = ref(false)
  /** 其后静默刷新失败：保留最后已知列表，标注说出口。 */
  const refreshError = ref(false)
  /** 是否成功拉到过一次（「首拉」与「静默刷新」的分界）。 */
  const hasLoaded = ref(false)
  /** 请求序号：手动刷新、轮询、筛选变化并发时，旧响应晚到不得覆盖新数据。 */
  let loadSeq = 0

  /** 相对时间的「现在」：随每次成功拉取前移（列表 5 秒一轮，时间文案跟着走即可，
      不为它单开 1 秒心跳 —— 事件流的 1 秒节流服务的是实时滚动，这里是静态受理时刻）。 */
  const nowSec = ref(Math.floor(Date.now() / 1000))

  const filter = ref<'all' | 'pending' | 'done'>('all')

  const state = computed<'loading' | 'error' | 'empty' | 'ready'>(() => {
    if (firstError.value) return 'error'
    if (loading.value && !hasLoaded.value) return 'loading'
    // 空态只认「全域/筛选下真的没有任务」（total=0）：total>0 而本页为空只可能是
    // 「这一页被并发清理清空了」的越界窗口（下一拍就被拉回有效页），此时说
    // 「没有进行中的任务」是一句假话 —— 宁可让列表空着等那一拍。
    if (hasLoaded.value && items.value.length === 0 && total.value === 0) return 'empty'
    return 'ready'
  })

  /** 空态文案：三档各说各的（「还没有受理过任何任务」是全域真空，「没有进行中的任务」
      与「没有已结束的任务」都是筛选结果 —— 已结束档曾说全量档的话，是文案错位）。 */
  const emptyText = computed(() => {
    if (filter.value === 'pending') return '没有进行中的任务'
    if (filter.value === 'done') return '没有已结束的任务'
    return '还没有受理过任何任务'
  })

  /**
   * 拉一次任务列表（筛选与分页作为 query 全部透传，服务端过滤/分页 —— 与容器列表页
   * 同一条纪律：前端不做本地过滤、不做本地切页）。
   */
  async function load(silent = false) {
    const seq = ++loadSeq
    if (!silent) loading.value = true
    try {
      const resp = await fetchDockerTasks({
        ...(filter.value === 'all' ? {} : { status: filter.value }),
        page: page.value,
        pageSize: pageSize.value
      })
      if (seq !== loadSeq) return
      items.value = resp.items ?? []
      total.value = resp.total ?? 0
      // 页参数以**服务端回显**为准（8d 契约）：服务端会夹页大小上限并据此切页，
      // 前端跟着它对同一句话（回显缺席时保留本地值 —— 老服务端的降级路径）。
      if (resp.page) page.value = resp.page
      if (resp.pageSize) pageSize.value = resp.pageSize
      hasLoaded.value = true
      firstError.value = false
      refreshError.value = false
      nowSec.value = Math.floor(Date.now() / 1000)
      // 越界页自愈：保留清理（或筛选切换时的位移）可能让当前页落空，回拉到最后一页
      // —— 分页器与「共 N 条」必须是同一句话的两个说法，不能出现「第 3 页 / 共 5 条」
      // 而列表空着。收敛性：回拉后的页必非空（total>0 时最后一页至少有 1 条），
      // 且页码真的变了才重拉（防「服务端返回与合计自相矛盾」时打成死循环）。
      if (items.value.length === 0 && total.value > 0 && page.value > 1) {
        const last = Math.max(1, Math.ceil(total.value / pageSize.value))
        if (last !== page.value) {
          page.value = last
          void load()
        }
      }
    } catch {
      if (seq !== loadSeq) return
      if (hasLoaded.value) {
        // 已有列表：静默失败保留旧数据 + 标注（不整页切错误态 —— 页面开着，把刚看完
        // 的列表整个换成错误屏反而丢上下文）。
        refreshError.value = true
      } else {
        firstError.value = true
      }
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  function manualRefresh() {
    void load()
  }

  // ── 5 秒轮询（页面可见期间；失活/卸载停表） ──────────────────
  const POLL_MS = 5000
  let timer: number | undefined

  function stopPoll() {
    if (timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }

  function restartPoll() {
    stopPoll()
    // 后台标签页的 setInterval 会被浏览器限流：隐藏期间跳过 tick（与总览自动刷新
    // 同一纪律），回到前台后最多一轮之内追上。
    timer = window.setInterval(() => {
      if (!document.hidden) void load(true)
    }, POLL_MS)
  }

  // 进页面即拉（旧数据超过 5 秒也是旧数据）+ 起表。
  onMounted(() => {
    void load()
    restartPoll()
  })

  // 筛选变化即重拉（服务端过滤没有本地回退，不重拉列表就不会变）；同时回到第 1 页
  // —— 筛选后的「第 3 页」没有意义（旧页码属于旧集合，且常常直接越界）。
  watch(filter, () => {
    page.value = 1
    void load()
  })

  // ── 分页（服务端分页；切页/切大小即重拉，不本地切） ──────────────
  // 切页大小回到第 1 页（同一理由：旧页码属于旧的一页一世界的集合）。两条都走
  // load（非 silent —— 页码在动，给一帧 loading 表意比静默换内容好）。
  function onCurrentChange(p: number) {
    if (p === page.value) return
    page.value = p
    void load()
  }

  function onSizeChange(s: number) {
    if (s === pageSize.value) return
    pageSize.value = s
    page.value = 1
    void load()
  }

  // keep-alive 失活停表（页面被 worktab 缓存时不轮询）；激活回来补拉 —— 后台期间
  // 浏览器限流了 interval，切回来的列表已是旧的。首次激活（= 挂载）不算「回来」：
  // onMounted 已经拉过并起了表，这里再拉就是同屏双请求（KeepAlive 内挂载时
  // activated 与 mounted 同时走，见 events-feed 的 start 幂等注释 —— 这里用标记闸）。
  let activatedOnce = false

  onDeactivated(stopPoll)

  onActivated(() => {
    const first = !activatedOnce
    activatedOnce = true
    if (first) {
      restartPoll() // 幂等：表已在走就重建一次（onMounted 已起过表），不加请求
      return
    }
    restartPoll()
    void load(true)
  })

  onBeforeUnmount(stopPoll)

  // ── 行渲染的映射（纯函数，测试经组件渲染断言钉住） ──────────
  // 状态文案与色收敛到 utils/status（与容器状态列/项目态同一来源）：taskStatusText
  // 出中文标签、taskStatusTone 出语义档，点/字色再经 statusDotColor / statusTextColor
  // （点 = EP 语义基色、字 = AA token）。本页只剩「进行中用旋转图标」这条结构判定。

  /** 进行中判定：pending 用旋转图标（8px 圆点放不下动画的表意），不是普通圆点。 */
  function isPending(status: string): boolean {
    return status === 'pending'
  }

  /** 动作的展示名：注册表 label 优先，查不到原样显示（调用处加等宽 class）。 */
  function actionLabel(item: DockerTaskItem): string {
    return lookupDockerAction(item.action)?.label ?? item.action
  }

  function isKnownAction(item: DockerTaskItem): boolean {
    return lookupDockerAction(item.action) !== undefined
  }

  /**
   * 目标的展示文本：长十六进制 id（容器/镜像的 64/40 位 id，可带 sha256: 前缀）
   * 截成 12 位短 id —— 模块「长 ID 短显 + title 全显」的惯例（container-detail
   * 的页头 id、events-feed 的 actor 兜底同款）；名称与镜像引用原样显示。全值仍在
   * title 里（完整引用是排障钥匙）。为什么不是只靠 CSS 省略：省略号截在哪由容器
   * 宽度决定，用户不知道被截掉了什么；短 id 是「按惯例这段就够用」的稳定显示。
   */
  const HEX_ID_RE = /^[0-9a-f]{32,}$/i

  function targetText(target: string): string {
    const bare = target.replace(/^sha256:/, '')
    return HEX_ID_RE.test(bare) ? bare.slice(0, 12) : target
  }

  /**
   * 是否有动作参数原文可看：**字段缺席 = 没有看这笔任务参数的资格**（服务端按任务
   * 自身权限码门控，缺席不是空串、不是掩码）——「缺席」与「空串」都不渲染（与全站
   * 「不渲染 ≠ 禁用」一致）。
   */
  function hasOptions(item: DockerTaskItem): boolean {
    return typeof item.options === 'string' && item.options !== ''
  }

  /**
   * 可展开的行：
   *   - **有动作参数原文**（options 存在）就展开 —— 这覆盖了「终态 exec 但 summary
   *     为空」这类行（没有参数块时它们既无进度流也无结论句，会点不动）；
   *   - 进行中的**进度族**任务（pull/build/push —— 与 core 的 progressSessionOf
   *     同源的三族）接实时进度流；凭 action 判定（后端 6b 契约：action 就是
   *     「可流/可取消」的依据，不需要额外的 streamable 字段）；
   *   - 有结论句的行（**任何终态**：failed/timeout 的排障线索，以及 succeeded
   *     的收尾结论原文 —— 服务端对成功任务同样给 summary，如「执行成功」「拉取
   *     完成：xxx」。修前只认失败/超时两态，成功态的任务看上去既不可展开也无
   *     详情入口，用户点不动就以为「没有输出」（P1 实测评审）。
   * 已展开的行即使转入终态也保留展开态（收尾结论就地接上，用户不用再点一次），
   * 收起按钮随之常驻。
   */
  const PROGRESS_ACTIONS = new Set(['image:pull', 'image:build', 'image:push'])

  function isProgressAction(item: DockerTaskItem): boolean {
    return PROGRESS_ACTIONS.has(item.action)
  }

  function isExpandable(item: DockerTaskItem): boolean {
    if (hasOptions(item)) return true
    if (item.status === 'pending' && isProgressAction(item)) return true
    // 终态：有结论句就展开（不限失败/超时 —— 成功同样有收尾结论）。
    if (item.status === 'pending') return false
    return item.summary !== ''
  }

  // ── 取消入口的可见性（展开区里的「取消」按钮）──────────────────────────
  // 三闸与服务端逐条对齐，前两道在**前端只是可见性**（服务端仍会复核）：
  //   - 任务属于我：core 的取消端点要求发起人归属（与进度流接入同档）——
  //     别人的任务在我这里不出现这个按钮（username 是服务端联查的用户名，删号
  //     降级为空串 → 不显示，fail-closed）；
  //   - 我有 docker:manage：进度三族的受理权限码，无它服务端必 403；
  //   - 任务还在 pending（终态的取消 = 409，不给无意义的按钮）。
  const { hasAuth } = useAuth()
  const userStore = useUserStore()
  const myUsername = computed(() => userStore.getUserInfo?.username ?? '')

  function canCancel(item: DockerTaskItem): boolean {
    if (item.status !== 'pending' || !isProgressAction(item)) return false
    if (!hasAuth(PermDockerManage)) return false
    return item.username !== '' && item.username === myUsername.value
  }

  const expandedRefs = ref<string[]>([])

  function isExpanded(ref: string): boolean {
    return expandedRefs.value.includes(ref)
  }

  function toggleExpand(item: DockerTaskItem) {
    const i = expandedRefs.value.indexOf(item.ref)
    if (i >= 0) expandedRefs.value.splice(i, 1)
    else expandedRefs.value.push(item.ref)
  }

  // 重新拉列表后，从窗口里消失的行带着展开态一起走（连带的进度流子组件卸载即断流
  // —— 那只是停止观看：任务照常跑完，展开态随行消失是列表窗口的事实；
  // 本页条目被切走（翻页/筛选/轮询位移）同理，见 task-progress 的取舍注释）。
</script>

<style lang="scss" scoped>
  /* 页面骨架（hero/三态/动效降级）曾复用列表页范式样式；列表页纯化后已无 hero
     与 wkl 卡外观，本页只留自己的列表样式。 */
  @use './overview-tokens' as t;
  // 列让位的阈值与模块列的 hideBelow 同源（断点单源在 @styles/core/breakpoints.scss）。
  @use '@styles/core/breakpoints.scss' as bp;

  // 「重试」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  // 主机 chip（胶囊）：主机是事实上的分组键（与容器/镜像/卷/网络/项目页同一全局类
  // docker-host + t.pill 制 —— 表体由 ElTable/原生表各自渲染，类挂全局）。
  :global(.docker-host) {
    @include t.pill;
  }

  /* 工具条左组：返回钮 + 状态过滤一行排开（对齐系统管理的 toolbar-left）。 */
  .toolbar-left {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  /* 本页的页面级对比度令牌（终审 QA D2 实测后的收口）：
     --el-text-color-secondary：元信息行的状态词/主机/发起人仍吃 EP 默认 #909399
     （对白底 3.08:1），页面范围内升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。 */
  .docker-tasks {
    --el-text-color-secondary: var(--el-text-color-regular);
  }

  /* 卡体收成 flex 列（矮视口修复要常驻，不能只在 short 档生效）：本页的单滚动链
     在 1280×800 这类「不矮但不算高」的档位上落不到（short 只到 640 高），卡体退回
     块级流 → 行区 + 分页总高超过卡体，仍被 overflow:hidden 裁掉分页（实测 1280×800
     卡体 667 / 内容 723，分页 bottom 835 > vh 800）。这里把卡体自己钉成 flex 列，
     行区与分页的分配在**任何视口高度**下都成立（正是 ArtTable 内那条链的同形）。 */
  :deep(.el-card__body) {
    display: flex;
    flex-direction: column;
    min-height: 0;
    overflow: hidden;
  }

  /* 首拉骨架：等价最小 loading（无卡外观），只给一层留白。 */
  .tk-loading {
    padding: 32px 16px;
  }

  /* 卡内错误/空态：给接近表格空态的留白（与镜像/卷/网络页 的 docker-empty 同款）。 */
  .docker-empty {
    padding: 56px 0;
  }

  // 静默刷新失败标注：琥珀即「需要注意」（与模块内陈旧/离线标注同一套颜色语言）。
  // flex:none —— 卡内固定块，不参与行区（.tk-list）的高度分配。
  .tk-refresh-error {
    flex: none;
    margin: 8px 0;
    // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token，
    // 数字见 @styles/core/aa-text.scss。
    color: var(--aa-warning-text);
    font-size: 12px;
    line-height: 1.6;
  }

  // 任务表所在的行区（分页在卡片尾部，见 .tk-page —— 一页就是「最近 pageSize 条」
  // 这一屏，翻页由服务端完成）。行区自己也是**表体的滚动容器**：表头吸顶靠它。
  //
  // **自持滚动（P0 修复，结构不动）**：本表的滚动目标就是这里，不是 .art-table，
  // 全局单滚动链（app.scss 的 art-single-scroll-chain）里那条 `.art-table{flex:1 1 0}`
  // 落不到它身上 —— 卡片体只裁不滚（.el-card__body{overflow:hidden}），矮视口下滚到底
  // 分页条仍在视口下方（844/1024/1366/1280 实测 bottom 742~835 > vh 390~800，
  // 末 2~3 条任务被裁且翻不了页）。这里把行区自己收成滚动目标：吃满卡内
  // 剩余高度（flex: 1 1 auto —— 不能写 basis:0：卡片随内容收缩时卡体没有确定
  // 高度，basis:0 会把行区直接压成 0）并自滚，分页条 flex:none 常驻卡底，
  // 与 ArtTable 的「表体自滚 + 分页钉底」同一口径（见
  // components/core/tables/art-table/index.vue 的同款修复）。
  .tk-list {
    flex: 1 1 auto;
    min-height: 0;
    margin: 0;
    padding: 0;
    overflow: auto;
    list-style: none;
  }

  // 表体：列式（可见表头 + 单行行高 + 展开区跨列）。表语言**沿用模块其余列表页的
  // 观感**（ArtTable/ElTable 实测口径：表头 14px/600/regular 色、单元格 14px、
  // 行高 49、分隔线与悬停都走 EP token），不新造配色。
  .tk-table {
    width: 100%;
    // 窄屏下限：低于它表体横向滚动（与 ArtTable 列 minWidth 溢出同款降级）。
    // 四档验收视口（844~1920）都在下限之上 —— 不出现横向滚动。
    min-width: 520px;
    // 固定布局：列宽由**表头一行**声明（同一个 class 也挂在每个单元格上），
    // 每一行因此落在同一组列线上 —— 这正是改造前缺的那件事。
    table-layout: fixed;
    // separate + 0 间距：sticky 表头的底边不被边框折叠吃掉（collapse 下 sticky
    // 行的边框会随滚动消失），分隔线改由每个单元格自己持有。
    border-collapse: separate;
    border-spacing: 0;
    color: var(--el-text-color-regular);
    font-size: 14px;
  }

  // 列宽：**百分比**（不是某个列吃满剩余宽度）—— 与模块表格「数据列按 minWidth
  // 比例分摊剩余宽度」同一观感。改造后的第一次实测暴露了反面：只让「目标」吃剩余
  // 宽度时，1920 下它一个人吞掉 1100px，空白从行中间挪到了列中间（同一处丑）。
  // 比例按「这一列最窄要多少」定（状态 11 / 动作 18 / 目标 25 / 主机 14 /
  // 发起人 12 / 时间 14 / 展开 6 = 100）：窄视口按比例缩，宽视口按比例摊。
  // 状态列的下限是硬线（圆点 + 两字状态词 + 内边距，844 实测 10% 会把「失败」截成
  // 「失…」——状态词是这条列表的结论，不许截），所以它拿到比其余列高一档的份额。
  .tk-col--status {
    width: 11%;
  }

  .tk-col--action {
    width: 18%;
  }

  .tk-col--target {
    width: 25%;
  }

  .tk-col--host {
    width: 14%;
  }

  .tk-col--user {
    width: 12%;
  }

  .tk-col--time {
    width: 14%;

    // 时间列右对齐 + 等宽数字：一列数字按位对齐才读得一列（模块 .is-num 同款）。
    &.is-num {
      font-variant-numeric: tabular-nums;
      text-align: right;
    }
  }

  // 展开列：开关靠右（与表右缘留 12px 一致的呼吸位；左右内边距的让位见 .tk-item__line
  // 里的同名覆盖 —— 它要盖过单元格的公共内边距，只能写在同一个选择器层级里）。
  .tk-col--toggle {
    width: 6%;
    text-align: right;
  }

  // 表头：吸顶（.tk-list 自滚 → 翻页/滚动时「这列是什么」始终在屏上），底色与
  // 字重照模块表头（次要一档的实心底，不是自创色）。
  .tk-head th {
    position: sticky;
    top: 0;
    z-index: 1;
    height: 40px;
    padding: 0 12px;
    background: var(--el-fill-color-lighter);
    box-shadow: inset 0 -1px 0 var(--el-border-color-lighter);
    color: var(--el-text-color-regular);
    font-size: 14px;
    font-weight: 600;
    text-align: left;
    white-space: nowrap;
  }

  // 列随视口收窄让位（阈值与模块列的 hideBelow 同源：发起人 < 1024、主机 < 768）。
  // 隐藏的是**整列**（表头与每个单元格同 class）：table-layout: fixed 下这一列连
  // 宽度一起消失，腾出的宽度归目标列。
  @include bp.respond-below('desktop') {
    .tk-col--user {
      display: none;
    }
  }

  @include bp.respond-below('tablet') {
    .tk-col--host {
      display: none;
    }
  }

  // 只在读屏里存在的列名（展开列的头格没有可见文字，不能没有名字）。
  .tk-sr {
    position: absolute;
    overflow: hidden;
    width: 1px;
    height: 1px;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  // 数据行：单行行高（改造前是两行块）+ 行分隔线 + 悬停整行高亮（表格语言）。
  // 单元格 nowrap + 截断：一行永远是一行，「目标」全值在 title 里。
  .tk-item__line {
    td {
      height: 49px;
      padding: 0 12px;
      overflow: hidden;
      border-bottom: 1px solid var(--el-border-color-lighter);
      text-overflow: ellipsis;
      vertical-align: middle;
      white-space: nowrap;
    }

    &:hover td {
      background: var(--el-fill-color-light);
    }

    // 两处列级内边距让位（必须写在 `.tk-item__line td` 同一层级才盖得住它的
    // 公共内边距）：
    //   - 展开列：24px 的开关放不进「窄列 + 两侧 12px」的内容盒，越界会在格子右缘
    //     多出一个省略号（实测 844：列宽 36 + 两侧内边距 24 → 内容盒 12 < 24）；
    //   - 状态列：点 + 两字状态词不需要右侧呼吸位（下一列自带左内边距）—— 这是
    //     844 档「失败」不被截成「失…」的那几 px。
    .tk-col--toggle {
      padding-left: 0;
    }

    .tk-item__status {
      padding-right: 0;
    }
  }

  // 展开中的行：整行压一档底色（与它下面那条同色的展开区连成一片）—— 同时展开
  // 好几条时「哪一行开着」一眼可辨；悬停仍给一档更亮的反馈（选择器权重更高，
  // 盖得住上面那条常驻底色）。
  .tk-item.is-open {
    .tk-item__line td {
      background: var(--el-fill-color-lighter);
    }

    .tk-item__line:hover td {
      background: var(--el-fill-color-light);
    }
  }

  // 展开区：整行（跨列铺满）的第二块，底色压一档与上面的行分开 —— 它是「这一行的
  // 详情」，不是列表里新的一行。
  .tk-col--detail {
    padding: 12px;
    border-bottom: 1px solid var(--el-border-color-lighter);
    background: var(--el-fill-color-lighter);
    white-space: normal;
  }

  // 状态列：圆点 + 状态词同列同排（颜色永不单独传达 —— 文字本身就是结论）。
  .tk-status {
    display: inline-flex;
    max-width: 100%;
    align-items: center;
    gap: 6px;

    &__text {
      // min-width:0 —— flex 项的自动最小尺寸是内容宽，不给它让位的话状态词会
      // 顶着裁剪线而不是走自己的省略号。
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
  }

  // 状态点：pending 是旋转图标，其余是 8px 圆点。圆点色不写死 —— 走内联的
  // statusDotColor(tone)（与状态文字同源一档），伪元素只画形（background:currentColor）。
  // 盒子 14px：装得下 14px 的旋转图标，又不吃掉状态词的宽度（状态列是本表的硬线）。
  .tk-item__dot {
    display: inline-flex;
    flex: none;
    width: 14px;
    height: 14px;
    align-items: center;
    justify-content: center;

    &::after {
      display: block;
      width: 8px;
      height: 8px;
      border-radius: 50%;
      content: '';
      background: currentColor;
    }

    // pending 用旋转图标（8px 圆点放不下动画的表意），不画圆点。
    &.is-pending::after {
      display: none;
    }
  }

  // 动作列：注册表查不到的 action 原样等宽显示（等宽即「这是原始码」的提示）。
  .tk-item__action {
    color: var(--el-text-color-primary);

    &.is-raw {
      font-family: var(
        --el-font-family-mono,
        ui-monospace,
        'SFMono-Regular',
        Consolas,
        monospace
      );
    }
  }

  // 目标列：等宽 + 截断（悬停看全名 —— 完整引用是排障钥匙，不给截断丢掉）。
  .tk-item__target {
    font-family: var(--art-font-family-mono);
  }

  // 发起人列：设备/账号已删时是「—」（不可考不是空）。
  .tk-item__user {
    color: var(--el-text-color-secondary);
  }

  // 展开开关：与行同高的小热区（图标按钮），可展开/已展开才出现。
  .tk-item__toggle {
    display: inline-flex;
    width: 24px;
    height: 24px;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: none;
    background: none;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
      color: var(--el-color-primary);
    }
  }

  // 终态结论句：展开区的非流形态（失败/超时的排障线索；收尾的拉取也落在这里）。
  .tk-item__summary {
    margin: 0;
    color: var(--el-text-color-regular);
    font-size: 12px;
    line-height: 1.6;
    word-break: break-all;
  }

  // 分页条（8d）：左侧一个硬事实（第 X 页 / 共 N 条），右侧分页控件 —— 同一个 total。
  .tk-page {
    flex: none;
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    align-items: center;
    justify-content: space-between;
    padding-top: 10px;
    border-top: 1px solid var(--el-border-color-lighter);
    background: var(--default-box-color);

    &__hint {
      color: var(--el-text-color-regular);
      font-size: 12px;
    }
  }

  // pending 的旋转动画：与总览/列表页加载态同一「转圈 = 在途」的表意。
  .tk-spin {
    animation: tk-rotate 1.2s linear infinite;
  }

  @keyframes tk-rotate {
    from {
      transform: rotate(0deg);
    }

    to {
      transform: rotate(360deg);
    }
  }

  // prefers-reduced-motion：旋转降级为静态图标（动效不参与信息表达时的兜底）。
  @media (prefers-reduced-motion: reduce) {
    .tk-spin {
      animation: none;
    }
  }
</style>
