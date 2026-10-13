<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       本页是「全局检索」整页（隐藏路由，从总览 hero 进）：工具条在表卡内（返回 +
       搜索框 + 搜索钮），五组结果在卡内 —— 与 tasks/events/image-drift 同款纯清单形态。 -->
  <div class="docker-search-page art-full-height">
    <ElCard class="art-table-card" shadow="never">
      <!-- :layout 显式去掉 columns 段（组件默认含）：本页不是 ArtTable，工具条没有
           列集合 —— 保留列设置钮只会开出空弹层（死控件）；其余段与组件默认一致。 -->
      <ArtTableHeader
        :loading="loading"
        layout="search,refresh,size,fullscreen,settings"
        @refresh="runSearch"
      >
        <template #left>
          <div class="toolbar-left">
            <!-- 返回上一页：本页没有菜单入口（从总览 hero 进），返回键就是它的出口。 -->
            <ArtButtonTable
              icon="ri:arrow-left-line"
              icon-class="bg-g-300/55 text-g-700"
              title="返回上一页"
              @click="back"
            />
            <!-- 搜索框：回车或点「搜索」触发（不发即查，免得敲字过程打满请求）。
                 空 q 不报错、不发请求，直接给五组空结果（契约的空 q 口径）。 -->
            <ElInput
              v-model="keyword"
              class="dsp-input"
              size="small"
              clearable
              placeholder="搜索容器 / 镜像 / 数据卷 / 网络 / Compose"
              @keyup.enter="runSearch"
              @clear="runSearch"
            >
              <template #prefix>
                <ArtSvgIcon icon="ri:search-line" />
              </template>
            </ElInput>
            <ElButton type="primary" size="small" :loading="loading" @click="runSearch">
              搜索
            </ElButton>
          </div>
        </template>
      </ArtTableHeader>

      <!-- 部分主机未参与检索（Wave 4d）：**本页最关键的诚实性设计** —— 采集失败的主机
           是一次「数据不全」的硬事实，不能让「搜到 3 条」掩盖「2 台主机没搜」。只列
           主机名 + reason 原文，不加解释句（页面零解释文案）。零值不渲染。 -->
      <p v-if="unavailableText" class="dsp-note">{{ unavailableText }}</p>

      <!-- 首拉/检索中：极简文本（结果面只是五组清单，不给骨架重卡）。 -->
      <p v-if="loading" class="dsp-loading">检索中…</p>
      <p v-else-if="error" class="dsp-error">检索失败，请重试</p>

      <!-- 未输入态：一句硬提示（怎么触发检索）。与「没有匹配」是两种事实，
           不能都渲染成五组「—」——否则「还没搜」看起来和「搜不到」一模一样。 -->
      <p v-else-if="!searched" class="dsp-prompt">输入关键字，回车或点搜索</p>

      <!-- 搜过了但五组全 0：无匹配结论（与未输入态的提示语逐字不同，一眼可辨）。 -->
      <p v-else-if="noMatch" class="dsp-empty">没有匹配的资源</p>

      <!-- 有结果（含部分组为空）：五组清单（顺序固定、空组也出现）。每组：标题带总数 +
           截断如实说出口 + 逐条（名称单行等宽 / 主机归属胶囊 / hint 次要线索 /
           可跳条目点击下钻到条目本身）。 -->
      <div v-else class="dsp-groups">
        <section v-for="g in displayGroups" :key="g.kind" class="dsp-group">
          <header class="dsp-group__head">
            <span class="dsp-group__label">{{ g.label }}</span>
            <span class="dsp-group__count">共 {{ g.total }} 条</span>
            <!-- 部分结果必须说出口（本模块既有纪律）：truncated 为真时如实报「仅显示前 N 条」。 -->
            <span v-if="g.truncated" class="dsp-group__trunc">
              仅显示前 {{ g.items.length }} 条
            </span>
          </header>

          <ul v-if="g.items.length" class="dsp-list">
            <li v-for="item in g.items" :key="`${item.hostId}/${item.name}`" class="dsp-item">
              <!-- 可跳条目：真按钮（键盘可达）。跳不成的（未知资源类型）退不可点的展示 ——
                   能跳则跳，不假装可跳。 -->
              <button
                v-if="g.route"
                type="button"
                class="dsp-name dsp-mono"
                :title="item.name"
                @click="go(g, item)"
              >
                {{ item.name }}
              </button>
              <span v-else class="dsp-name dsp-mono" :title="item.name">{{ item.name }}</span>
              <span class="docker-host">{{ hostText(item) }}</span>
              <span v-if="item.hint" class="dsp-hint truncate" :title="item.hint">
                {{ hintText(item.hint) }}
              </span>
            </li>
          </ul>
          <p v-else class="dsp-group__empty">—</p>
        </section>
      </div>
    </ElCard>
  </div>
</template>

<script setup lang="ts">
  /**
   * 全局跨资源检索页（Wave 4d 收尾件，隐藏路由 `/docker/search`）。
   *
   * 一个关键字跨容器/镜像/数据卷/网络/Compose **五类**资源子串匹配（名称；容器另
   * 匹配镜像名、镜像按 tag 串）—— 数据源 GET /docker/search。每组**独立限量**、逐组
   * 告知 `truncated`（部分结果说出口）；采集失败的主机落 `unavailableHosts`，页面
   * 据此提示 —— **不能让「搜到 3 条」掩盖「2 台主机没搜」**。groups 固定五组、顺序
   * 固定、空组也出现（空数组非 null）。
   *
   * 两处诚实性 / 形态取舍：
   *   - 空 q：**不发请求**（后端虽返回五空组，前端直接回到未输入态）、**不报错**；
   *   - 三种结果面彼此可辨：未输入 → 「输入关键字，回车或点搜索」；搜过但全 0 →
   *     「没有匹配的资源」；有结果 → 五组清单。此前三态同形（都是五组「—」），
   *     未输入与搜不到看起来一模一样。
   *   - 可跳条目一律 path 形态 push（菜单页注册名 `menu-<menuId>`，插件名没注册 ——
   *     命名跳转静默失败）。下钻**落到条目本身**（用条目必填的 `id`）：容器 / 镜像落
   *     各自详情页（镜像 id 含冒号，作路由参数必须编码），数据卷 / 网络落列表页并带
   *     `?host=&keyword=<name>`（这两页没有详情页，至少按名把清单收窄到那一条），
   *     Compose 落工作台 `/docker/projects/:name?host=`。
   *
   * 入口在**总览 hero**（图标钮），与任务中心/事件流/版本一致性同款 —— 不加后端菜单
   * 种子，路由取 isHide 形态（理由见 index.ts）。
   */
  import { computed, ref } from 'vue'
  import { useRouter, type RouteLocationRaw } from 'vue-router'
  import { ElButton, ElCard, ElInput } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import {
    fetchDockerSearch,
    type DockerSearchGroup,
    type DockerSearchItem,
    type DockerUnavailableHostItem
  } from '../api'

  defineOptions({ name: 'DockerSearch' })

  const router = useRouter()

  /** 返回上一页：本页没有菜单入口（从总览 hero 进），返回键就是它的出口。 */
  function back() {
    router.back()
  }

  // ── 五组的固定顺序与展示元数据（标签 + 下钻目标）────────────────────────
  // 顺序固定：契约保证后端也按此序给，但前端**再钉一遍**（防御 + 空组也补齐）。
  const GROUP_ORDER = ['containers', 'images', 'volumes', 'networks', 'projects'] as const

  interface GroupMeta {
    label: string
    /** 条目 → 下钻目标（path 形态）。返回 undefined = 该条目不可跳（不可点展示）。 */
    route: (item: DockerSearchItem) => RouteLocationRaw
  }

  // 下钻目标按「落地真能定位到那条」定（条目带必填 `id`）：
  //   - 容器 → 容器详情页 `/docker/containers/:id`（容器 id 是十六进制，编码是恒等）；
  //   - 镜像 → 镜像详情页 `/docker/image-detail/:id` —— 镜像 id 是 `sha256:<64>`，
  //     含冒号，作路由参数**必须编码**（详情页 decodeURIComponent 解回同一口径）；
  //   - 数据卷 / 网络：没有详情页 → 落各自列表页并带 `?host=&keyword=<name>`，按名
  //     把清单收窄到那一条（keyword 是这两页 searchForm 里既有的字段）；
  //   - Compose → 工作台 `/docker/projects/:name?host=`（名字在条目里，精确落位）。
  // 一律 path 形态 push（菜单页注册名是 menu-<menuId>，插件名没注册 —— 命名跳转静默失败）。
  const GROUP_META: Record<string, GroupMeta> = {
    containers: {
      label: '容器',
      route: (i) => ({
        path: `/docker/containers/${encodeURIComponent(i.id)}`,
        query: { host: i.hostId }
      })
    },
    images: {
      label: '镜像',
      route: (i) => ({
        path: `/docker/image-detail/${encodeURIComponent(i.id)}`,
        query: { host: i.hostId }
      })
    },
    volumes: {
      label: '数据卷',
      route: (i) => ({ path: '/docker/volumes', query: { host: i.hostId, keyword: i.name } })
    },
    networks: {
      label: '网络',
      route: (i) => ({ path: '/docker/networks', query: { host: i.hostId, keyword: i.name } })
    },
    projects: {
      label: 'Compose',
      route: (i) => ({ path: `/docker/projects/${i.name}`, query: { host: i.hostId } })
    }
  }

  interface DisplayGroup {
    kind: string
    label: string
    total: number
    truncated: boolean
    items: DockerSearchItem[]
    /** 缺省 = 该组不可跳（未知资源类型），条目走不可点展示。 */
    route?: (item: DockerSearchItem) => RouteLocationRaw
  }

  // ── 数据与状态 ──────────────────────────────────────────────────────────
  const keyword = ref('')
  const groups = ref<DockerSearchGroup[]>([])
  const unavailableHosts = ref<DockerUnavailableHostItem[]>([])
  const loading = ref(false)
  const error = ref(false)
  /** 是否已用**非空**关键字检索过：区分「未输入」（提示语）与「搜了但全 0」（无匹配）。
   *  初始 / 清空后为 false —— 未输入态与「没有匹配」必须一眼可辨，不能都渲染成五组「—」。 */
  const searched = ref(false)
  /** 请求序号：连点搜索时旧响应晚到不得覆盖新结果。 */
  let loadSeq = 0

  /**
   * 检索一次。空 q（trim 后）不发请求、不报错，直接回到**未输入态**（不再给五组空
   * 结果 —— 未输入与「没有匹配」是两种事实，混同一屏会让人以为搜过了）。
   * 非空 q 走 GET /docker/search（服务端过滤 + 分组限量），失败给就地错误态（不弹
   * toast：搜索框就开着，结论就地显示，见 api.ts 的同款纪律）。
   */
  async function runSearch() {
    const term = keyword.value.trim()
    if (!term) {
      groups.value = []
      unavailableHosts.value = []
      error.value = false
      loading.value = false
      searched.value = false
      return
    }
    const seq = ++loadSeq
    loading.value = true
    error.value = false
    try {
      const resp = await fetchDockerSearch({ q: term })
      if (seq !== loadSeq) return
      groups.value = resp.groups ?? []
      unavailableHosts.value = resp.unavailableHosts ?? []
      searched.value = true
    } catch {
      if (seq !== loadSeq) return
      error.value = true
      groups.value = []
      unavailableHosts.value = []
      searched.value = false
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  /** 五组展示模型：固定顺序补齐（后端缺的组补空）+ 未知类型追加在末尾（不可跳）。 */
  const displayGroups = computed<DisplayGroup[]>(() => {
    const byKind = new Map(groups.value.map((g) => [g.kind, g]))
    const known = GROUP_ORDER.map((kind) => ({ kind: kind as string, raw: byKind.get(kind) }))
    const extra = [...byKind.keys()]
      .filter((k) => !(GROUP_ORDER as readonly string[]).includes(k))
      .map((kind) => ({ kind, raw: byKind.get(kind) }))
    return [...known, ...extra].map(({ kind, raw }) => ({
      kind,
      label: GROUP_META[kind]?.label ?? kind,
      total: raw?.total ?? 0,
      truncated: raw?.truncated ?? false,
      items: raw?.items ?? [],
      route: GROUP_META[kind]?.route
    }))
  })

  /** 已检索过但五组全 0：给「没有匹配的资源」（与「未输入」的提示语是两句话）。 */
  const noMatch = computed(() => !displayGroups.value.some((g) => g.total > 0))

  /** 部分主机未参与检索：只列主机名 + reason 原文（页面零解释文案）。零值不渲染。 */
  const unavailableText = computed(() =>
    unavailableHosts.value.length === 0
      ? ''
      : `${unavailableHosts.value.length} 台主机未参与检索：` +
        unavailableHosts.value.map((h) => `${h.hostname || h.hostId}（${h.reason}）`).join('、')
  )

  /** 主机归属文本：主机名 → hostId。 */
  function hostText(item: DockerSearchItem): string {
    return item.hostname || item.hostId
  }

  /**
   * hint 的展示文本：镜像摘要是 `sha256:<64 位>`——按模块惯例（image-drift 同款）
   * 剥前缀取前 12 位短显，**不把摘要算法名/长十六进制直铺到页面上**；其余 hint
   * （卷/网络的驱动、容器的镜像引用）原样显示。全文仍在 title 里。
   */
  function hintText(hint?: string): string {
    if (!hint) return ''
    if (hint.startsWith('sha256:')) return hint.slice('sha256:'.length, 'sha256:'.length + 12)
    return hint
  }

  /** 条目下钻：path 形态 push（菜单页注册名没注册，命名跳转静默失败）。 */
  function go(group: DisplayGroup, item: DockerSearchItem) {
    if (!group.route) return
    void router.push(group.route(item))
  }
</script>

<style lang="scss" scoped>
  @use './overview-tokens' as t;

  // 主机 chip（胶囊）：与镜像/容器/卷/网络/项目/漂移页同一全局类 docker-host + t.pill 制。
  :global(.docker-host) {
    @include t.pill;
  }

  .docker-search-page {
    /* 次要文字对比度 AA（逐页口径）：EP 默认 --el-text-color-secondary(#909399) 对
       白底只有 3.08:1，低于 AA 正文线 → 页面范围内升到 regular 档。 */
    --el-text-color-secondary: var(--el-text-color-regular);

    .toolbar-left {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }

    // 搜索框：给个够宽的稳定宽度（结果组标题与条目都靠它对齐）。
    .dsp-input {
      width: min(420px, 60vw);
    }

    // 未参与检索的主机 / 检索失败：琥珀即「需要注意」（与模块内陈旧/离线标注同一套）。
    .dsp-note,
    .dsp-error {
      margin: 8px 0;
      font-size: 12px;
      line-height: 1.6;
      color: var(--aa-warning-text);
    }

    .dsp-loading {
      margin: 8px 0;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    // 未输入态提示（怎么触发检索）与无匹配结论：两句硬话，彼此不同、也都不是空块。
    .dsp-prompt,
    .dsp-empty {
      margin: 8px 0;
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    // 工具行（返回 + 搜索框 + 搜索钮）**常驻卡顶**：卡被页根高度截住时不得被压扁
    // —— 让位的只能是结果区这一节（否则搜索框会被裁，用户连改关键字都做不到）。
    :deep(#art-table-header) {
      flex: none;
    }

    // 未参与检索提示行与四种结果态的提示语同样是卡内常驻块（不参与高度分配、不进
    // 滚动区）：「N 台主机未参与检索」掉出卡外就失去了它的**诚实性**作用。
    .dsp-note,
    .dsp-loading,
    .dsp-error,
    .dsp-prompt,
    .dsp-empty {
      flex: none;
    }

    // 五组结果：竖排分区，组间以分隔线隔开。
    //
    // **自持滚动（本页唯一滚动目标）**：本页不是 ArtTable（结果面是五组自绘清单），
    // 拿不到全局单滚动链里 `.art-table-card .art-table { flex: 1 1 auto }` 那一节的高度
    // 分配 —— 卡体只裁不滚（`.art-table-card .el-card__body { overflow: hidden }`），
    // 于是结果一多就被**裁掉**、全页还没有任何可滚的层（1920×1080 实测：卡体
    // clientH 855 / scrollH 1174，末条 bottom 1353 落在视口外，滚轮够不到）。
    // 这里把结果区自己收成滚动目标：吃满卡内剩余高度（flex: 1 1 auto —— 不能写
    // basis:0：卡片随内容收缩时卡体没有确定高度，basis:0 会把结果区直接压成 0）并在
    // 内容超出时自滚。
    //   装得下：卡自身 `flex: 0 1 auto` 随内容收缩 → 卡体没有多余高度可分，结果区 =
    //   内容高，**零滚动条**（不出「空白 + 滚动条并存」）；装不下：卡被页根按高度截住
    //   （min-height: 0），结果区收缩到卡体剩余高度并自滚。
    // 两侧都不需要任何像素常数：chrome 变（提示行出现/搜索框换行）时高度跟着实测的
    // 剩余空间走（本模块刚因 `calc(… - 256px)` 栽过一次，事件流页已修 —— 不重蹈）。
    .dsp-groups {
      display: flex;
      flex: 1 1 auto;
      flex-direction: column;
      min-height: 0;
      overflow: auto;
    }

    .dsp-group {
      padding: 10px 0;

      & + & {
        border-top: 1px solid var(--el-border-color-lighter);
      }

      &__head {
        display: flex;
        align-items: baseline;
        flex-wrap: wrap;
        gap: 8px;
      }

      &__label {
        font-size: 13px;
        font-weight: 600;
        color: var(--el-text-color-primary);
      }

      &__count {
        font-size: 12px;
        color: var(--el-text-color-secondary);
      }

      // 截断标注：如实说出口（部分结果不得被「共 N 条」掩盖）。
      &__trunc {
        font-size: 12px;
        color: var(--aa-warning-text);
      }

      &__empty {
        margin: 4px 0 0;
        font-size: 12px;
        color: var(--el-text-color-secondary);
      }
    }

    .dsp-list {
      margin: 4px 0 0;
      padding: 0;
      list-style: none;
    }

    // 一条结果：名称 + 主机胶囊 + hint。内容单行不折行（hint 长值省略号截断）。
    .dsp-item {
      display: flex;
      align-items: center;
      gap: 10px;
      min-width: 0;
      padding: 4px 0;
      font-size: 13px;
    }

    // 名称：等宽（容器名/仓库引用/卷名都是「原始标识」）+ 单行省略（全文在 title）。
    .dsp-name {
      flex: none;
      max-width: 50%;
      overflow: hidden;
      padding: 0;
      border: none;
      background: none;
      color: var(--el-text-color-primary);
      font-family: var(--art-font-family-mono);
      font-size: 13px;
      text-align: left;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    // 可跳条目就是按钮：给 hover/focus 以表明可点。
    button.dsp-name {
      cursor: pointer;

      &:hover {
        color: var(--aa-primary-text-hover);
        text-decoration: underline dotted;
        text-underline-offset: 3px;
      }

      &:focus-visible {
        outline: 2px solid var(--el-color-primary);
        outline-offset: 2px;
        border-radius: 3px;
      }
    }

    // hint（次要线索：镜像名 / tag / 驱动等）：占满剩余宽度，长值省略号。
    .dsp-hint {
      flex: 1;
      min-width: 0;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }
  }
</style>