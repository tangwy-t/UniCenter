<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="docker-events art-full-height">
    <!-- 过滤面板：与模块其余列表页**同一形态**（卡外 ArtSearchBar）。本页此前是卡内
         自建的一行裸排控件（「主机」文字 + ElSelect + ElRadioGroup + input）——
         容器、标签、控件高度都自成一派，与容器/镜像/卷/网络/项目页的搜索面板不是
         一个观感。三项筛选的**维度与匹配口径一字未动**：主机（全部主机 + 各台）、
         类型（全部 + 容器/镜像/卷/网络）、关键词（对象 / 动作），全部服务端过滤
         （参数名与取值口径见 utils/events 的 matchesEventFilter 与 core 端点）。 -->
    <ArtSearchBar
      v-show="showSearchBar"
      v-model="searchForm"
      :items="searchItems"
      @search="applyFilter"
      @reset="resetFilter"
    />

    <ElCard class="art-table-card" shadow="never">
      <!-- :layout 显式去掉 columns 段（组件默认含）：本页不是 ArtTable（事件清单
           是自建 ElTable + 列插槽），工具条没有列集合 —— 保留列设置钮只会开出空弹层
           （死控件）；其余段与组件默认一致。showSearchBar 接线后，工具条上的搜索钮
           = 过滤面板的显隐开关（与模块其余列表页同一枚钮、同一语义）。 -->
      <ArtTableHeader
        v-model:showSearchBar="showSearchBar"
        :loading="loading"
        :has-active-filter="isFiltered"
        layout="search,refresh,size,fullscreen,settings"
        @refresh="manualRefresh"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- 返回上一页：本页没有菜单入口（从活动流面板头部进），返回键就是它的出口。 -->
            <ArtButtonTable
              icon="ri:arrow-left-line"
              icon-class="bg-g-300/55 text-g-700"
              title="返回上一页"
              @click="back"
            />
            <!-- 流的连接态（硬数据，不是解释句）：实时 / 连接中 / 已断开·重连中 /
                 已停止·原因。表意走模块既有的「在途」语汇 —— 连接过程给旋转图标
                 （loader 转圈 = 在途，与任务页 pending 同源），稳态给实心点；
                 点**不单独表意**，旁边永远有文字结论。原 hero 的呼吸点（只靠颜色
                 表意）已删，这里不复活它。 -->
            <span class="ev-status" :class="`is-${phase}`">
              <ArtSvgIcon
                v-if="phase === 'connecting' || phase === 'reconnecting'"
                icon="ri:loader-4-line"
                class="ev-status__spin"
              />
              <span v-else class="ev-status__dot" aria-hidden="true" />
              {{ statusText }}
            </span>
            <!-- 实时跟随 / 暂停：暂停 = **冻结列表**（新事件只计数不插行）——列表是
                 「最新在前」的表格，插行会把正在读的位置整段顶下去；恢复 = 把暂停期间
                 的 N 条一次并回来。流本身不暂停（暂停只停「列表动」，不停「收事实」）。 -->
            <ElButton
              size="small"
              class="ev-follow"
              :class="{ 'is-paused': paused }"
              @click="togglePause"
            >
              <ArtSvgIcon
                :icon="paused ? 'ri:play-line' : 'ri:pause-line'"
                class="ev-follow__icon"
              />
              {{ paused ? '继续跟随' : '暂停' }}
            </ElButton>
            <!-- 暂停事实（可点即恢复）：整条是一条**事实陈述**，不是解释句。收进工具行
                 而非原来那条跨列横幅 —— 横幅要独占 49px（旧口径为保证「账目句不被挤出
                 屏」必须常备预留），并进工具行后这 49px 回到表体，暂停时页面也不再跳一下。 -->
            <button v-if="paused" type="button" class="ev-paused" @click="togglePause">
              {{ pausedCount > 0 ? `已暂停 · 新事件 ${pausedCount} 条` : '已暂停' }}
            </button>
            <!-- 致命失败（401/403）的重试：与活动流面板同款（重试换不回结果时才停机）；
                 停机原因在左边的连接态里原文可读。 -->
            <ElButton v-if="phase === 'stopped' && stopReason" size="small" @click="retryStream">
              重试
            </ElButton>
          </div>
        </template>
      </ArtTableHeader>

      <!-- ── 首拉加载（还没有任何行可给）：等价最小 loading（骨架）——
           不再有页面级加载卡外观（列表页纯化：加载态的表意交给列表自身）。 ── -->
      <div v-if="state === 'loading'" class="ev-skeleton">
        <ElSkeleton :rows="6" animated />
      </div>

      <!-- ── 首拉失败：紧凑结论态 —— 图标 + 结论句 + 重试（不显示旧列表 —— 那时还没有
           旧列表可显示；全局错误 toast 由 utils/http 的 request() 统一弹出）。 ── -->
      <div v-else-if="state === 'error'" class="ev-empty docker-empty">
        <ArtSvgIcon icon="ri:error-warning-line" class="ev-empty__icon is-error" />
        <p class="ev-empty__fact">事件历史获取失败</p>
        <ElButton size="small" @click="manualRefresh">重试</ElButton>
      </div>

      <!-- ── 空态：两种事实分开说 —— 「实时流已连接、还没有事件」（流是活的，等
           事实到达）与「筛选没命中」（有事件，只是不合尺），都不是解释句。
           形态是紧凑结论块（小图标 + 一行事实 + 可选动作），不是原来那张占满
           半屏的通用 ElEmpty 插画。 ── -->
      <div v-else-if="state === 'empty'" class="ev-empty docker-empty">
        <ArtSvgIcon :icon="emptyIcon" class="ev-empty__icon" />
        <p class="ev-empty__fact">{{ emptyFact }}</p>
        <ElButton v-if="isFiltered" size="small" @click="resetFilter">清除筛选</ElButton>
      </div>

      <!-- ── 正常态：静默失败标注 + 事件表 + 翻页 ── -->
      <template v-else>
        <!-- 静默刷新失败：保留最后已知列表并说出口（与列表页「上次数据仍在」同一口径）。 -->
        <p v-if="refreshError" class="ev-refresh-error">刷新失败，正在显示上次结果</p>
        <!-- 高度：不设 max-height —— 表体高度由**真实剩余高度**决定（.ev-table 那条
             flex 链，见样式里「表：接回 flex 剩余高度」）。内容装得下 → 表 = 内容高、
             卡随内容收缩、无内滚；装不下 → 卡被页根截住、表头不动、表体自滚，游标条
             （账目句 + 加载更早）常驻卡底。本页的翻页是游标式（无分页器），行数不能
             顶走游标条。 -->
        <ElTable :data="rows" size="small" class="ev-table" :row-class-name="() => 'ev-row'">
          <!-- 时刻：绝对 + 相对（同一个 t 的两种读法，两行同一格；完整挂钟仍在
               title 里 —— 相对时间换算不回事件序号，精确账是可引用的那一半）。
               列宽按「19 字符挂钟」定，窄档优先保它不被截。 -->
          <ElTableColumn label="时刻" min-width="140">
            <template #default="{ row }">
              <div class="ev-time" :title="row.clock">
                <span class="ev-time__abs">{{ row.clock }}</span>
                <span class="ev-time__rel">{{ eventRelativeTime(row.tSec, nowSec) }}</span>
              </div>
            </template>
          </ElTableColumn>
          <!-- 类型：四类订阅维度的图标 + 中文名（色即类别，未知走中性）。文字色走
               模块的 AA token（点鲜字深）—— 直接拿 EP 基色当文字色在浅色底上
               过不了 4.5:1（warning 1.85、info 3.08）。 -->
          <ElTableColumn label="类型" min-width="84">
            <template #default="{ row }">
              <span class="ev-type" :class="`is-${row.tone}`">
                <ArtSvgIcon :icon="row.icon" />
                {{ row.typeLabel }}
              </span>
            </template>
          </ElTableColumn>
          <!-- 动作：**中文结论为主、原始动作码为辅**（旧版两段同色并排，「销毁destroy」
               连成一串看起来像错字）。原始码压一档（等宽小字 + 次要色，title 给全），
               并且窄列里先让位 —— 中文结论是这条列表的读法，不许被截。 -->
          <ElTableColumn label="动作" min-width="120">
            <template #default="{ row }">
              <div
                class="ev-act"
                :title="
                  row.action === row.rawAction ? row.action : `${row.action} · ${row.rawAction}`
                "
              >
                <span class="ev-action">{{ row.action }}</span>
                <span class="ev-raw ev-mono">{{ row.rawAction }}</span>
              </div>
            </template>
          </ElTableColumn>
          <!-- 对象：短显 + 截断（悬停看全 —— 完整引用是排障钥匙）。 -->
          <ElTableColumn label="对象" min-width="160">
            <template #default="{ row }">
              <span class="ev-actor ev-mono" :title="row.actorTitle">{{ row.actor }}</span>
            </template>
          </ElTableColumn>
          <!-- 主机：模块统一的主机胶囊（与容器/镜像/卷/网络/项目/任务页同一全局类
               docker-host）—— 跨主机表的行归属，不再是最弱的一段灰文本。 -->
          <ElTableColumn label="主机" min-width="104">
            <template #default="{ row }">
              <span class="docker-host">
                <span class="ev-host" :title="`主机 ${row.hostId}`">{{ row.host }}</span>
              </span>
            </template>
          </ElTableColumn>
          <!-- 退出码：一列数字按位对齐（右对齐 + 等宽）；die 系才有；0 中性、≠0 红染；
               nil 显示「—」（不可考不是 0，也不猜一个 0）。 -->
          <ElTableColumn label="退出码" min-width="96" align="right">
            <template #default="{ row }">
              <span v-if="row.exitText" class="ev-exit" :class="`is-${row.exitTone}`">
                {{ row.exitText }}
              </span>
              <span v-else class="ev-exit is-none">—</span>
            </template>
          </ElTableColumn>
        </ElTable>

        <!-- 翻页（游标）：只报两个硬事实 —— 已加载多少、窗口共多少；还有更早的
             才给「加载更早」（服务端 nextCursor 为空的结束态）。
             「窗口共 N」以历史首拉落定（hasLoaded）为闸：落定前窗口数是缺位
             事实（total 初值 0），报出来就是「已加载 N · 窗口共 0」的当场矛盾。 -->
        <div class="ev-more">
          <span class="ev-more__hint">
            已加载 {{ rows.length }} 条<template v-if="hasLoaded">
              · 窗口共 {{ total }} 条</template
            >
          </span>
          <ElButton v-if="nextCursor" size="small" :loading="loadingMore" @click="loadMore">
            加载更早
          </ElButton>
        </div>
      </template>
    </ElCard>
  </div>
</template>

<script setup lang="ts">
  /**
   * 事件流详版页（本波，`/docker/events`）：活动流面板的整页形态 —— 跨主机 docker
   * 事件的**历史 + 实时**一张表，回答「刚才到现在都发生了什么、谁在哪台机器上退出成
   * 什么样」。照 views/tasks.vue 先例：hidden 路由（无菜单种子，入口长在总览的活动流
   * 面板头部）＋ docker:list（与两条端点同档）。本波「列表页纯化」后 = 表卡（工具条：
   * 返回 + 连接态 + 暂停/继续 + 重试；事件表与游标条在卡内）的纯清单形态，页头
   * （hero：图标+标题+副标题+呼吸点）整块删除。
   *
   * 本波（事件流页优化）改的是**读法**，不是数据面：
   *   - 过滤条从卡内自建行换成卡外 ArtSearchBar（模块统一形态；维度/选项/参数/
   *     匹配口径一字未动）；
   *   - 列宽按内容比例分摊（数据列一律 min-width，无固定宽度）：旧口径里「对象」
   *     是唯一的弹性列，1920 下单列独吞 ~1000px，主机与退出码被推到最右、整行稀疏；
   *   - 动作列分主辅层级（中文结论为主、原始码次要），退出码右对齐，主机走统一胶囊；
   *   - 空态分两种事实且收成紧凑结论块（去掉占半屏的通用插画）；
   *   - 暂停事实从跨列横幅收进工具行（那 49px 还给表体，暂停时页面不再跳）；
   *   - 表体高度**不再用写死的 `-256px` 常数猜上方 chrome**（猜错就是「表体比内容
   *     矮 + 卡片下还空着一块」并存、低视口切行）：事件表接回模块统一的 flex 剩余
   *     高度链（.ev-table，见样式「表：接回 flex 剩余高度」）——内容装得下随内容
   *     收缩、装不下表头不动表体内滚。
   *
   * 数据是**两条腿**（core 的同一份保留窗口）：
   *   - 历史：GET /docker/events/history（t 降序、游标分页、服务端过滤）；
   *   - 实时：GET /docker/events（NDJSON；连接回放各主机最近 50 条 + 实时推送）。
   * 加载序固定为**先历史、后实时**：列表先有「刚才」的完整一段（含连接前几秒的空窗），
   * 再接流 —— 反过来的话，回放段会先于历史段到达，列表要先被重放灌一遍再被历史覆盖。
   * 两段合成一条时间轴、按内容复合键去重（utils/events 的 createEventsLog，那里有
   * 论证与测试）：流的回放与刚拉的历史大量重叠，不去重就是双份。
   *
   * 过滤是**服务端过滤**（端点参数）＋ 实时行的**客户端过滤**（流是全量的，页面筛了
   * 就得自己拦下不合的行；谓词与 core 的 keyword 同款，见 matchesEventFilter）。
   * 过滤条件变化 = 重建时间轴（旧列表属于旧筛选，留着只会让人以为「筛了没生效」）
   * ＋重拉历史页 1；流不动（它本来就不带筛选）。**输入即生效**（事件流是「盯着看」
   * 的页面，收窄的手感就是即时）—— 面板上的「查询」钮是同一条路径的显式入口
   * （立即重查：重建时间轴并重拉窗口），「重置」= 清空三项。
   *
   * 实时跟随 / 暂停：暂停**冻结列表**（新事件只计数），恢复一次性并回 —— 列表是
   * 「最新在前」的表，插行会把阅读位置整段顶下去；流始终不停（暂停停的是「列表动」，
   * 不是「收事实」）。断流的自动重连与页面的 keep-alive 生命周期照活动流面板同款
   * 纪律（连接 = 激活期；失活即断流，回来重连 + 补拉历史）。
   *
   * 时刻列给的是**事件的 agent 挂钟**（core 透传的 t，与活动流面板同源）—— 它可能在
   * 时钟偏斜的主机上偏移；窗口裁剪走的是 core 接收时刻（后端侧），列表不拿两种时间
   * 混排（同一列只画一个口径）。
   */
  import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
  import { useRouter } from 'vue-router'
  import { ElButton, ElCard, ElSkeleton, ElTable, ElTableColumn } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSearchBar from '@/components/core/forms/art-search-bar/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import {
    fetchDockerEventHistory,
    openDockerEventsStream,
    type DockerEventHistoryItem
  } from '../api'
  import { useHostList } from '../composables/useResourceList'
  import { hostLabel } from '../utils/host'
  import {
    createEventsLog,
    eventActionText,
    eventClockTime,
    eventRelativeTime,
    eventTypeLabel,
    eventTypeMeta,
    exitCodeText,
    exitCodeTone,
    matchesEventFilter,
    parseHistoryItem,
    type DockerEventFields,
    type DockerEventFilter,
    type DockerEventItem
  } from '../utils/events'

  defineOptions({ name: 'DockerEventsPage' })

  const router = useRouter()

  /** 返回上一页：本页没有菜单入口（从活动流面板头部进），返回键就是它的出口。 */
  function back() {
    router.back()
  }

  // ── 过滤（三项服务端过滤 + 实时行客户端过滤）────────────────────
  /** 过滤面板显隐（工具条搜索钮切换）；与本模块其余列表页同款默认展开。 */
  const showSearchBar = ref(true)
  /** 过滤表单（ArtSearchBar 的 v-model）：**变化即生效**，见下面的 watch。 */
  const searchForm = ref<{ keyword?: string; type?: string; host?: string }>({})

  const { hosts, loadHosts } = useHostList()

  /** 主机选项（label 走模块统一的 hostLabel；主机分组存在时按组折叠）。 */
  const hostOptions = computed(() =>
    hosts.value.map((h) => ({ label: hostLabel(h), value: h.id, group: h.group || '未分组' }))
  )

  /**
   * 过滤项：关键词（对象 / 动作）→ 类型（四档）→ 主机（全部主机 + 各台）。
   *
   * 类型从旧版的四档 ElRadioButton 换成 select：ArtSearchBar 的面板里，四档互斥
   * 枚举走 select 是本模块的统一口径（容器页的状态、网络页的仅内部网络同源），
   * 「全部」= 清空（clearable），与其余页的「全部 X」占位一致。
   */
  const searchItems = computed(() => [
    {
      key: 'keyword',
      label: '关键词',
      type: 'input',
      placeholder: '对象 / 动作',
      clearable: true
    },
    {
      key: 'type',
      label: '类型',
      type: 'select',
      placeholder: '全部类型',
      clearable: true,
      options: [
        { label: '容器', value: 'container' },
        { label: '镜像', value: 'image' },
        { label: '卷', value: 'volume' },
        { label: '网络', value: 'network' }
      ]
    },
    {
      // 主机维度是筛选项：全部主机 + 各台。
      // filterable：主机多了要能敲名字找，与容器/镜像/卷/网络/项目页同款。
      key: 'host',
      label: '主机',
      type: 'select',
      placeholder: '全部主机',
      clearable: true,
      filterable: true,
      options: hostOptions.value
    }
  ])

  /** 当前过滤（发给端点的形态：空值不发；关键词去空白 —— 空白串不是筛选）。 */
  const activeFilter = computed<DockerEventFilter>(() => ({
    hostId: searchForm.value.host ?? '',
    type: searchForm.value.type ?? '',
    keyword: (searchForm.value.keyword ?? '').trim()
  }))

  const isFiltered = computed(() => {
    const f = activeFilter.value
    return f.hostId !== '' || f.type !== '' || (f.keyword ?? '') !== ''
  })

  // ── 时间轴（历史 ∪ 实时，t 降序，内容复合键去重）─────────────────
  // log 用 let 而非 const：过滤变化时整条重建（旧筛选的行不该留在新列表里）。
  let log = createEventsLog({ accept: (f) => matchesEventFilter(f, activeFilter.value) })
  const entries = ref<readonly DockerEventItem[]>([])
  /** 冻结态的行快照（暂停期间列表不动；恢复时丢弃它、回到 log.entries）。 */
  const frozen = ref<readonly DockerEventItem[] | null>(null)
  /** 暂停起点：pausedCount = 暂停以来的接受条数（不拿「列表长度」相减 —— 上限
      裁剪会让长度不再等量增长，差值会凭空变大）。 */
  let pausedBase = 0

  /** 窗口共 N 条 = 端点报的窗口全量（抓取时刻）+ 此后实时接受的条数；历史首拉
      落定（hasLoaded）前这个数不出口 —— 缺位事实不报（模板闸门见翻页条注释）。 */
  const total = ref(0)
  /** 最近一次历史抓取完成时的 log 计数（实时增量的基准）。 */
  let liveBase = 0
  let windowTotal = 0
  const nextCursor = ref('')

  const paused = ref(false)
  const pausedCount = ref(0)

  /** 相对时间的「现在」：1 秒一跳（tick 在生命周期里起停）。 */
  const nowSec = ref(Math.floor(Date.now() / 1000))

  // ── 四态与账目 ───────────────────────────────────────────
  const loading = ref(false)
  const loadingMore = ref(false)
  const firstError = ref(false)
  const refreshError = ref(false)
  const hasLoaded = ref(false)
  /** 请求序号：手动刷新、过滤切换、轮询并发时，旧响应晚到不得覆盖新数据。 */
  let loadSeq = 0

  const state = computed<'loading' | 'error' | 'empty' | 'ready'>(() => {
    if (firstError.value) return 'error'
    // 骨架态覆盖两种「列表正在被替换」：首拉（还没有任何行）与筛选重建（旧筛选的
    // 行属于旧查询，重建期间给骨架比给一片旧行诚实）。
    if (loading.value) return 'loading'
    if (hasLoaded.value && entries.value.length === 0 && total.value === 0) return 'empty'
    return 'ready'
  })

  /**
   * 空态事实（两句话是两种不同的处境，不合并）：
   *   - 筛了没命中：有事件，只是不合尺 —— 给「清除筛选」；
   *   - 没筛、流是活的：「实时流已连接 · 等待事件」—— 流连着、窗口里还没有事实；
   *   - 没筛、流不在线（重连中/已停止/连接中）：只报窗口这一件事实（不冒充「已连接」）。
   */
  const emptyFact = computed(() => {
    if (isFiltered.value) return '没有匹配的事件'
    if (phase.value === 'live') return '实时流已连接 · 等待事件'
    return '窗口内没有事件'
  })

  const emptyIcon = computed(() => {
    if (isFiltered.value) return 'ri:search-line'
    if (phase.value === 'live') return 'ri:radar-line'
    return 'ri:history-line'
  })

  // ── 行渲染模型（纯函数算一次，模板不逐行调函数）──────────────────
  interface EventRow {
    seq: number
    icon: string
    tone: string
    typeLabel: string
    action: string
    rawAction: string
    actor: string
    actorTitle: string
    host: string
    hostId: string
    exitText: string
    exitTone: string
    tSec: number
    clock: string
  }

  function shortActorId(id: string): string {
    return id.replace(/^sha256:/, '').slice(0, 12)
  }

  /** 长十六进制 id（容器/镜像的 64/40 位 id，可带 sha256: 前缀）。 */
  const HEX_ID_RE = /^[0-9a-f]{32,}$/i

  /**
   * 对象列展示文本：**缩短而非截断**。
   *
   * 病灶（评审 D）：某些事件的 `actor_name` 本身就是一串完整 sha256（71 字符 =
   * `sha256:` + 64 位），直接渲染会把整串 id 铺满一格。这里对「看起来是十六进制 id」
   * 的取值一律取前 12 位短 id（模块「长 id 短显」惯例）；名称（容器名/镜像名/网络名）
   * 原样显示。全值仍在 title 里（完整引用是排障钥匙）。
   */
  function shortActor(name: string, id: string): string {
    if (name) {
      const bare = name.replace(/^sha256:/, '')
      return HEX_ID_RE.test(bare) ? bare.slice(0, 12) : name
    }
    return shortActorId(id) || '—'
  }

  const rows = computed<EventRow[]>(() => {
    const src = frozen.value ?? entries.value
    return src.map((e) => {
      const meta = eventTypeMeta(e.type)
      const shortId = shortActorId(e.actorId)
      return {
        seq: e.seq,
        icon: meta.icon,
        tone: meta.tone,
        typeLabel: eventTypeLabel(e.type),
        action: eventActionText(e.action),
        rawAction: e.action,
        actor: shortActor(e.actorName, e.actorId),
        actorTitle:
          e.actorName && shortId ? `${e.actorName} · ${shortId}` : e.actorId || e.actorName || '—',
        host: e.hostname || `主机 ${e.hostId}`,
        hostId: e.hostId,
        exitText: e.exitCode !== null ? exitCodeText(e.exitCode) : '',
        exitTone: e.exitCode !== null ? exitCodeTone(e.exitCode) : 'neutral',
        tSec: e.t / 1000,
        clock: eventClockTime(e.t)
      }
    })
  })

  // ── 历史加载（首拉 / 静默刷新 / 筛选重建 / 翻页）──────────────────
  /**
   * 拉一页历史。三种形态共用一个函数（差别只在 rebuild 与游标）：
   *   - 首拉 / 筛选切换：rebuild=true —— **重建时间轴**（旧筛选的行属于旧查询，
   *     留着只会让人以为「筛了没生效」），骨架态显示；
   *   - 手动刷新：rebuild=false、游标清空 —— 抓窗口最新一页并进现有时间轴
   *     （重复由内容键去重），列表**不清空**（静默刷新：已有行不闪）；
   *   - 翻页（加载更早）：rebuild=false、带上游标。
   */
  async function loadHistory(opts: { rebuild?: boolean; cursor?: string } = {}) {
    const rebuild = opts.rebuild === true
    const cursor = opts.cursor ?? ''
    const seq = ++loadSeq
    if (rebuild) {
      loading.value = true
      loadingMore.value = false
    } else if (cursor !== '') {
      loadingMore.value = true
    }
    try {
      const f = activeFilter.value
      const resp = await fetchDockerEventHistory({
        ...(f.hostId ? { hostId: f.hostId } : {}),
        ...(f.type ? { type: f.type } : {}),
        ...(f.keyword ? { keyword: f.keyword } : {}),
        ...(cursor ? { cursor } : {})
      })
      if (seq !== loadSeq) return
      const items: DockerEventFields[] = []
      for (const raw of resp.items ?? []) {
        const fields = parseHistoryItem(raw as DockerEventHistoryItem)
        if (fields) items.push(fields) // 形状漂移的行跳过，不打断整页
      }
      if (rebuild) {
        // 重建时间轴：新 log 的 accept 闭包读的是**当前**过滤，历史种子与随后
        // 到的实时行走同一把尺（见 utils/events 的 createEventsLog）。
        log = createEventsLog({ accept: (ff) => matchesEventFilter(ff, activeFilter.value) })
        frozen.value = null
        paused.value = false
        pausedCount.value = 0
        entries.value = []
      }
      log.seedHistory(items)
      liveBase = log.totalSeen // 实时增量的基准：这次抓取之后到的才算「新增」
      windowTotal = resp.total ?? 0
      nextCursor.value = resp.nextCursor ?? ''
      hasLoaded.value = true
      firstError.value = false
      refreshError.value = false
      nowSec.value = Math.floor(Date.now() / 1000)
      syncView()
    } catch {
      if (seq !== loadSeq) return
      if (hasLoaded.value && !rebuild) {
        // 已有列表（静默刷新/翻页失败）：保留旧数据 + 标注，不整页切错误态 ——
        // 页面开着，把刚看完的列表整个换成错误屏反而丢上下文。
        refreshError.value = true
      } else {
        firstError.value = true
      }
    } finally {
      if (seq === loadSeq) {
        loading.value = false
        loadingMore.value = false
      }
    }
  }

  /**
   * 工具条刷新（内置钮）：刷新 = 把眼前这一屏看新 —— 主机清单（筛选源）与事件
   * 窗口一起重拉（主机可能刚入库，只看事件会留下过期的筛选源；与其余列表页的
   * refreshAll 同语义）。事件窗口走非重建档：已有行不闪（静默刷新）。
   */
  function manualRefresh() {
    void loadHosts()
    void loadHistory()
  }

  function loadMore() {
    if (!nextCursor.value || loadingMore.value) return
    void loadHistory({ cursor: nextCursor.value })
  }

  /** 「查询」：过滤**输入即生效**（见下面的 watch），这枚钮是同一条路径的显式入口 ——
      立即重查（重建时间轴并重拉窗口），对实时流页面是「把窗口重新读一遍」的实动作。 */
  function applyFilter() {
    showSearchBar.value = true
    void loadHistory({ rebuild: true })
  }

  /** 「重置」：清空三项。清空本身会触发下面的 watch（重建 + 重拉）——已经没得清时
      补一次，保证「重置」永远是一次真实动作。 */
  function resetFilter() {
    const had = isFiltered.value
    searchForm.value = {}
    showSearchBar.value = true
    if (!had) void loadHistory({ rebuild: true })
  }

  /**
   * 过滤变化的**身份串**（watch 的键）。
   *
   * 为什么不是 watch(activeFilter)：那个 computed 每次求值都返回新对象，watch 按
   * 引用比较 —— 关键词加一个尾随空格也会「变了」，白打一次端点。键取去空白后的三项
   * 拼串，只在**有效过滤**真的变了时才重拉。
   */
  const filterKey = computed(() => {
    const f = activeFilter.value
    return `${f.hostId}\u0000${f.type}\u0000${f.keyword ?? ''}`
  })

  /**
   * 过滤变化即重拉（服务端过滤没有本地回退；实时行由新 log 的 accept 拦）。
   */
  watch(filterKey, () => {
    void loadHistory({ rebuild: true })
  })

  // ── 实时流（fetch + ReadableStream，断流指数退避重连）──────────────
  type Phase = 'connecting' | 'live' | 'reconnecting' | 'stopped'
  const phase = ref<Phase>('connecting')
  const stopReason = ref('')

  let connSeq = 0
  let streamAbort: AbortController | null = null
  let retryTimer: number | undefined
  let tickTimer: number | undefined
  let attempts = 0
  let liveSince = 0

  const RECONNECT_BASE_MS = 1_000
  const RECONNECT_MAX_MS = 10_000
  const STABLE_STREAM_MS = 30_000

  /** 把 log 的当前状态同步进渲染（行与两个账目）。暂停时行不动、只累计新事件数。 */
  function syncView() {
    // 「窗口共 N 条」= 端点报的窗口全量 + 此后实时接受的条数（两个数说的是同一
    // 窗口、同一过滤，只是抓取时刻不同 —— 相加不越界：实时行同样在窗口里）。
    total.value = windowTotal + Math.max(0, log.totalSeen - liveBase)
    if (paused.value) {
      pausedCount.value = Math.max(0, log.totalSeen - pausedBase)
      return
    }
    entries.value = log.entries
  }

  function stopTick() {
    if (tickTimer !== undefined) {
      clearInterval(tickTimer)
      tickTimer = undefined
    }
  }

  function startTick() {
    stopTick()
    tickTimer = window.setInterval(() => {
      // 后台标签页的 setInterval 会被浏览器限流：隐藏期间跳过 tick（与总览同一纪律）。
      if (!document.hidden) nowSec.value = Math.floor(Date.now() / 1000)
    }, 1_000)
  }

  function clearRetry() {
    if (retryTimer !== undefined) {
      clearTimeout(retryTimer)
      retryTimer = undefined
    }
  }

  function openConclusion(status: number): string {
    if (status === 401) return '登录状态已失效，请重新登录后再试'
    if (status === 403) return '没有接入事件流的权限'
    return ''
  }

  async function connect() {
    const seq = ++connSeq
    phase.value = 'connecting'
    stopReason.value = ''
    const controller = new AbortController()
    streamAbort = controller
    try {
      const res = await openDockerEventsStream(controller.signal)
      if (seq !== connSeq) return
      if (!res.ok || !res.body) {
        const fatal = openConclusion(res.status)
        if (fatal) {
          phase.value = 'stopped'
          stopReason.value = fatal
          return
        }
        scheduleReconnect()
        return
      }
      phase.value = 'live'
      liveSince = Date.now()

      const reader = res.body.getReader()
      const decoder = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (seq !== connSeq || done) break
        log.pushRaw(decoder.decode(value, { stream: true }))
        syncView()
      }
      if (seq !== connSeq) return
      log.pushRaw(decoder.decode())
      syncView()
      scheduleReconnect()
    } catch {
      if (seq !== connSeq) return
      scheduleReconnect()
    } finally {
      if (seq === connSeq) streamAbort = null
    }
  }

  function scheduleReconnect() {
    if (liveSince > 0 && Date.now() - liveSince >= STABLE_STREAM_MS) attempts = 0
    attempts++
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** (attempts - 1), RECONNECT_MAX_MS)
    phase.value = 'reconnecting'
    clearRetry()
    retryTimer = window.setTimeout(() => {
      retryTimer = undefined
      void connect()
    }, delay)
  }

  /** 断流（失活 / 卸载；幂等 —— 没有在飞就不扰动）。 */
  function stopStream() {
    connSeq++
    clearRetry()
    stopTick()
    const controller = streamAbort
    streamAbort = null
    if (controller) controller.abort()
  }

  /** 连接态文本（工具条里的硬数据）：停机原因原文可读（与重试钮成对出现）。 */
  const statusText = computed(() => {
    if (phase.value === 'stopped')
      return stopReason.value ? `已停止 · ${stopReason.value}` : '已停止'
    if (phase.value === 'reconnecting') return '已断开 · 重连中'
    if (phase.value === 'connecting') return '连接中…'
    return '实时'
  })

  // ── 跟随 / 暂停 ─────────────────────────────────────────
  function togglePause() {
    if (paused.value) {
      paused.value = false
      frozen.value = null
      pausedCount.value = 0
      entries.value = log.entries // 恢复：把暂停期间的 N 条一次并回来
      return
    }
    paused.value = true
    pausedBase = log.totalSeen // 暂停起点：新事件数从这一刻起算
    pausedCount.value = 0
    frozen.value = entries.value // 冻结列表：行不动，计数照走
  }

  // ── 生命周期（连接 = 页面激活期）────────────────────────────
  /**
   * 起一次「历史 + 实时」：先拉历史，再接流（加载序见文件头）。
   * 幂等：流已在飞（挂载与首次激活会同时走到这里）时只补一次历史，不重复接流；
   * startSeq 兜住「两次 start 交错」—— 前一次的 connect 不再执行（否则两条流并存）。
   */
  let startSeq = 0
  async function start() {
    const seq = ++startSeq
    startTick()
    if (streamAbort !== null || retryTimer !== undefined) {
      void loadHistory()
      return
    }
    await loadHistory({ rebuild: !hasLoaded.value })
    if (seq !== startSeq) return
    await connect()
  }

  function stop() {
    startSeq++ // 在飞的 start 作废（其 connect 不再执行）
    stopStream()
  }

  function retryStream() {
    if (phase.value !== 'stopped') return
    attempts = 0
    void connect()
  }

  // 挂载 / keep-alive 失活 / 激活 —— 与活动流面板同一套纪律（连接 = 激活期），
  // 外加「激活回来补拉历史」（失活期间的历史缺口由这一次抓取补上）。
  let activatedOnce = false

  onMounted(() => {
    // 主机清单（筛选下拉的选项源）由 useHostList 自己 onMounted 拉；这里只起数据面。
    void start()
  })

  onDeactivated(stop)
  onBeforeUnmount(stop)
  onActivated(() => {
    // 首次激活与挂载同帧（KeepAlive 内挂载）：onMounted 已经起过，不重复。
    if (!activatedOnce) {
      activatedOnce = true
      return
    }
    void start()
  })
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  /* 页面骨架（hero/三态）范式样式不再引用（列表页纯化，wkl 卡外观已删）；
     只借 overview-tokens 的按钮对比度处方与主机胶囊。 */
  @use './overview-tokens' as t;

  // 「重试」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  /* 工具条左组：返回钮 + 连接态 + 暂停/继续 + 暂停事实（对齐系统管理的 toolbar-left）。 */
  .toolbar-left {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  /* 页面级对比度令牌（与 tasks 页同款收口）：
     --el-text-color-secondary 升 regular：表内次要文字（相对时间/主机/退出码中性档）
     在白卡上吃 EP 默认 #909399 只有 3.08:1。 */
  .docker-events {
    --el-text-color-secondary: var(--el-text-color-regular);
  }

  .ev-mono {
    font-family: var(--art-font-family-mono);
  }

  /* 主机是事实上的分组键（与容器/镜像/卷/网络/项目/任务页同一全局类 docker-host
     + t.pill 制 —— 表体由 ElTable 渲染，类挂全局）。 */
  :global(.docker-host) {
    @include t.pill;
  }

  /* ---------- 工具行：连接态（点 + 文字同列，颜色永不单独传达） ---------- */
  .ev-status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--el-text-color-regular);

    /* 稳态点：14px 盒装 8px 圆（与任务页状态点同盒），色只画形、文字给结论。 */
    &__dot {
      display: inline-block;
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: var(--el-color-success);
    }

    /* 在途（连接中/重连中）：转圈即「在途」（与任务页 pending 同一表意）。 */
    &__spin {
      font-size: 14px;
      animation: ev-rotate 1.2s linear infinite;
    }

    /* 重连中：琥珀（需要注意，不是错误）。 */
    &.is-reconnecting {
      color: var(--aa-warning-text);
    }

    /* 停机（401/403）：红色陈述事实 —— 与旁边的重试钮成对。 */
    &.is-stopped {
      color: var(--aa-danger-text);

      .ev-status__dot {
        background: currentColor;
      }
    }
  }

  @keyframes ev-rotate {
    from {
      transform: rotate(0deg);
    }

    to {
      transform: rotate(360deg);
    }
  }

  /* prefers-reduced-motion：旋转降级为静态图标（动效不参与信息表达时的兜底）。 */
  @media (prefers-reduced-motion: reduce) {
    .ev-status__spin {
      animation: none;
    }
  }

  /* ---------- 跟随 / 暂停 ---------- */
  .ev-follow {
    .ev-follow__icon {
      margin-right: 4px;
    }

    /* 暂停中：这枚钮是「回到实时」的入口，给主色描边（在途的对照面）。 */
    &.is-paused {
      color: var(--aa-primary-text);
      border-color: var(--aa-primary-text);
    }
  }

  /* 暂停事实（整条可点即恢复）：琥珀底一条 —— 硬数据，不是解释句。 */
  .ev-paused {
    padding: 3px 10px;
    border: 1px solid transparent;
    border-radius: 999px;
    background: color-mix(in srgb, var(--el-color-warning) 14%, transparent);
    color: var(--aa-warning-text);
    font-size: 12px;
    white-space: nowrap;
    cursor: pointer;
  }

  /* 首拉骨架：等价最小 loading（无卡外观），只给一层留白。 */
  .ev-skeleton {
    padding: 32px 16px;
  }

  /* ---------- 紧凑结论态（空态 / 首拉失败）----------
     旧版是一张通用 ElEmpty 大插画（占半屏、页面没数据时非常空），且两种空态只差
     一行字。这里是「小图标 + 一行事实 + 可选动作」的紧凑块：事实是主角，插画退场。 */
  .docker-empty {
    padding: 0;
  }

  .ev-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 40px 16px;

    &__icon {
      font-size: 22px;
      color: var(--el-text-color-secondary);

      &.is-error {
        color: var(--aa-danger-text);
      }
    }

    &__fact {
      margin: 0;
      color: var(--el-text-color-regular);
      font-size: 13px;
      line-height: 1.6;
    }
  }

  /* 静默刷新失败标注：琥珀即「需要注意」（与 tasks 页同一套颜色语言）。
     同样不参与收缩：这是一句结论，卡被截住时不该被压掉（让位的只有表体）。 */
  .ev-refresh-error {
    flex: none;
    margin: 8px 0;
    color: var(--aa-warning-text);
    font-size: 12px;
    line-height: 1.6;
  }

  /* ---------- 表：接回 flex 剩余高度（本页不再猜 chrome 高度）----------
     页根 → 卡 → 卡体这条链在 app.scss 里已经就位（.art-full-height 定高 /
     .art-table-card 随内容收缩 / .el-card__body 是 flex 列分派容器），本页此前
     只差**表根这一节**：事件表是自建 ElTable（不是 ArtTable），拿不到全局那条
     `.art-table-card .art-table { flex: 1 1 auto; min-height: 0 }`，于是本页自己
     写了个 max-height 常数（`calc(var(--art-full-height) - 256px)`）去猜上方
     chrome —— 猜错时表体被压得比内容矮、卡片下方却还空着一块（空白与滚动条并存，
     同一个错的两个面），低视口下还会切掉行。

     这里就把那节补上，表体高度从此只由**真实剩余高度**决定：
       - 内容装得下：卡是 `flex: 0 1 auto`（随内容收缩），卡体没有多余高度可给，
         表停在内容高 —— 表体 = 内容高，零内滚；
       - 装不下：卡被页根按高度截住（min-height: 0），表根按卡体剩余高度收缩，
         表头（EP 的 header-wrapper 自带 flex-shrink: 0）不动，表体（EP 内层
         el-scrollbar，wrap 是 overflow: auto）自滚。
     两侧都不需要任何像素常数：chrome 变（告警条/换行/工具栏多一枚钮）时，卡与表
     的高度跟着实测的剩余空间走。

     basis 取 auto（不是 0）：卡随内容收缩时卡体没有确定高度，basis:0 会把表塌成 0
     （与全局链同一条理由）；卡被拉满的矮视口/全屏态下它照常吃掉剩余高度。 */
  .ev-table {
    flex: 1 1 auto;
    min-height: 0;
  }

  /* 工具行（ArtTableHeader 根）与游标条不参与高度收缩：**让位的只能是表体这一节**。
     否则卡被截住时先压扁工具行与账目行 —— 表头会被裁、「加载更早」会掉出卡外，
     而这两样正是必须始终可见可点的。 */
  :deep(#art-table-header) {
    flex: none;
  }

  /* ---------- 表 ---------- */
  .ev-row {
    cursor: default;
  }

  /* 行内一律单行（EP 的 .cell 默认 white-space:normal + break-all 会让长值折行堆高
     行；表是清单，不是段落）——超长内容走自己的省略号，全值在 title 里。 */
  .ev-time,
  .ev-act,
  .ev-actor,
  .ev-host,
  .ev-type {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .ev-time {
    display: flex;
    flex-direction: column;
    line-height: 1.4;

    &__abs {
      color: var(--el-text-color-primary);
      font-size: 12px;
      font-variant-numeric: tabular-nums;
    }

    &__rel {
      color: var(--el-text-color-secondary);
      font-size: 11px;
    }
  }

  /* 类型徽标：四类订阅维度的图标 + 中文名，色即类别（未知走中性）。
     文字色走 AA token（点鲜字深）—— EP 基色直接当文字色在浅色底上过不了 4.5:1
     （warning 1.85、info 3.08），模块的文字色口径是 --aa-*-text。 */
  .ev-type {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12px;

    &.is-container {
      color: var(--aa-primary-text);
    }

    &.is-image {
      color: var(--aa-warning-text);
    }

    &.is-volume {
      color: var(--aa-info-text);
    }

    &.is-network {
      color: var(--aa-success-text);
    }

    &.is-other {
      color: var(--el-text-color-regular);
    }
  }

  /* 动作单元格：中文结论（主）+ 原始动作码（辅）。 */
  .ev-act {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }

  /* 中文结论：主档、不许被截（原始码先让位）。 */
  .ev-action {
    flex: none;
    color: var(--el-text-color-primary);
    font-size: 13px;
  }

  /* 原始动作码：等宽小字压一档（等宽即「这是原始码」的提示），窄列里先让位
     （min-width:0 才能让 flex 项走自己的省略号），全值在 title 里。 */
  .ev-raw {
    flex: 0 1 auto;
    min-width: 0;
    color: var(--el-text-color-secondary);
    font-size: 11px;
  }

  /* 对象：等宽 + 截断（悬停看全引用）。 */
  .ev-actor {
    display: block;
    color: var(--el-text-color-regular);
    font-size: 12px;
  }

  /* 主机胶囊里的主机名：窄列里自己走省略号（胶囊形制不动）。 */
  .ev-host {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* 退出码：一列数字按位对齐（右对齐 + 等宽 + tabular）；0 中性、≠0 红染；
     无值（nil）给「—」（不可考不是 0）。 */
  .ev-exit {
    font-family: var(--el-font-family-mono, monospace);
    font-size: 12px;
    font-variant-numeric: tabular-nums;

    &.is-neutral {
      color: var(--el-text-color-regular);
    }

    &.is-danger {
      color: var(--aa-danger-text);
    }

    &.is-none {
      color: var(--el-text-color-secondary);
    }
  }

  /* ---------- 翻页（游标） ---------- */
  .ev-more {
    display: flex;
    flex: none; /* 常驻卡底：不参与高度分配（账目句与「加载更早」永远在卡内可见） */
    flex-wrap: wrap;
    gap: 10px;
    align-items: center;
    justify-content: space-between;
    padding-top: 10px;
    border-top: 1px solid var(--el-border-color-lighter);

    &__hint {
      color: var(--el-text-color-regular);
      font-size: 12px;
    }
  }
</style>
