<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。
       三个 tab 的确认弹窗/对话框随各自的 tab 组件收在它自己的单根里，
       页面级只剩这一个根。 -->
  <div class="docker-resources-page art-full-height overflow-y-auto">
    <div class="wkl-page__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：图标 + 标题 + 副标题 + 刷新（对齐容器页/总览）============ -->
      <div class="wkl-hero mb-4 flex flex-wrap items-center gap-3">
        <div class="wkl-hero__icon flex-cc">
          <ArtSvgIcon :icon="pageIcon" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-semibold text-[var(--el-text-color-primary)]">镜像与存储</h2>
          <p class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs text-g-600">
            <span
              class="live-dot inline-block h-1.5 w-1.5 rounded-full bg-success"
              :class="{ 'is-loading': hostsLoading }"
            />
            <span>{{ subtitle }}</span>
          </p>
        </div>
        <div class="ml-auto flex items-center gap-1">
          <!-- 页面级刷新：当前 tab 的清单 + 主机清单一起重拉（数据源是各 tab 自己的
               聚合端点，按钮的意义是「把眼前这页看新」，不是重拉某一份共享快照）。 -->
          <ArtButtonTable
            icon="ri:refresh-line"
            iconClass="bg-theme/12 text-theme"
            title="刷新"
            @click="refreshAll"
          />
        </div>
      </div>

      <!-- ============ 三个 tab（跨主机聚合表）============
           主机维度从页面级上下文（HostSwitcher）降为**每个 tab 筛选里的一项** ——
           与容器统一表同一范式：主机列给归属、主机筛选给收窄，`?host=` 深链作
           筛选初始值（总览磁盘面板与镜像详情返回链路的既有链路不动）。 -->
      <ElTabs v-model="activeTab" class="docker-resources-tabs">
        <ElTabPane name="images" lazy>
          <template #label>
            <span class="docker-resources-tab-label">
              <ArtSvgIcon icon="ri:box-1-line" />
              镜像
            </span>
          </template>
          <ImagesTab ref="imagesTabRef" :hosts="hosts" :hosts-loading="hostsLoading" />
        </ElTabPane>
        <ElTabPane name="volumes" lazy>
          <template #label>
            <span class="docker-resources-tab-label">
              <ArtSvgIcon icon="ri:hard-drive-3-line" />
              数据卷
            </span>
          </template>
          <VolumesTab ref="volumesTabRef" :hosts="hosts" :hosts-loading="hostsLoading" />
        </ElTabPane>
        <ElTabPane name="networks" lazy>
          <template #label>
            <span class="docker-resources-tab-label">
              <ArtSvgIcon icon="ri:share-forward-line" />
              网络
            </span>
          </template>
          <NetworksTab ref="networksTabRef" :hosts="hosts" :hosts-loading="hostsLoading" />
        </ElTabPane>
      </ElTabs>
    </div>
  </div>
</template>

<script setup lang="ts">
  /**
   * 镜像与存储页（9b：跨主机化，同容器统一表范式）。
   *
   * 数据源从「页面级一份单主机快照（useDockerHostState + HostSwitcher）」换成
   * 各 tab 自己的跨主机聚合端点（GET /docker/images|volumes|networks，9a）；
   * 主机从页面级上下文降为**筛选下拉**的一项（筛选形态对齐容器页），行归属由
   * 新增的主机列给出。`?host=` 深链照旧有效：作为各 tab 的**主机筛选初始值**
   * （总览磁盘面板的 goHostImages/goHostVolumes 与镜像详情的返回链路都不动）。
   *
   * 页面职责只有两件事（编排，不含业务）：
   *   1. **页面级一份主机清单**：useHostList 在这里调用一次，三个 tab 经 props
   *      共用（切 tab 不重复拉 —— 7a 收敛收益在跨主机形态下的延续）；tab 自己
   *      管自己那条聚合端点的拉取。
   *   2. **当前 tab 记在 URL query**（?tab=images|volumes|networks）：刷新与分享
   *      链接都能还原现场；切换只写 tab，不碰 host（host 从此是筛选状态）。
   *
   * 主机切换的重置纪律（原三页 onHostSwitch 的正文）随「页面级主机」概念一起
   * 消失：写操作按**行主机/筛选主机**派发（一次操作属于发起时锁定的那台主机），
   * 没有「切了主机要清什么」的问题。
   */
  import { computed, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElTabPane, ElTabs } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { usePageIcon } from '@/hooks/core/usePageIcon'
  import ImagesTab from '../components/resources/images-tab.vue'
  import VolumesTab from '../components/resources/volumes-tab.vue'
  import NetworksTab from '../components/resources/networks-tab.vue'
  import { useHostList } from '../composables/useResourceList'

  defineOptions({ name: 'DockerResources' })

  const route = useRoute()
  const router = useRouter()
  // 页头图标与侧边栏/页签同源（取菜单图标，改「菜单管理」即同步；见 usePageIcon）。
  const pageIcon = usePageIcon('ri:database-2-line')

  // ── 页面级一份主机清单（三 tab 的筛选下拉与「尚无可管主机」判定）────────
  const { hosts, hostsLoading, loadHosts } = useHostList()

  const subtitle = computed(() =>
    hostsLoading.value && hosts.value.length === 0
      ? '正在拉取主机清单'
      : `跨 ${hosts.value.length} 台主机`
  )

  // ── 当前 tab：URL query 是唯一事实源（深链/刷新还原；与 host 同一取向）────

  type ResourcesTabKey = 'images' | 'volumes' | 'networks'
  const TAB_KEYS: ResourcesTabKey[] = ['images', 'volumes', 'networks']
  /** 非法值（拼错/历史参数）一律回落镜像 tab —— 那是页面的默认视图。 */
  function normalizeTab(raw: unknown): ResourcesTabKey {
    return typeof raw === 'string' && TAB_KEYS.includes(raw as ResourcesTabKey)
      ? (raw as ResourcesTabKey)
      : 'images'
  }

  const activeTab = ref<ResourcesTabKey>(normalizeTab(route.query.tab))

  // query → tab：地址栏直改（深链/浏览器后退）时跟上。
  watch(
    () => route.query.tab,
    (raw) => {
      const tab = normalizeTab(raw)
      if (tab !== activeTab.value) activeTab.value = tab
    }
  )
  // tab → query：用户点 tab 时写回（replace 不进历史 —— tab 切换不该占用后退键）。
  watch(activeTab, (tab) => {
    if (String(route.query.tab ?? '') !== tab) {
      void router.replace({ query: { ...route.query, tab } })
    }
  })

  // ── 页面级刷新 ───────────────────────────────────────────────

  const imagesTabRef = ref<InstanceType<typeof ImagesTab> | null>(null)
  const volumesTabRef = ref<InstanceType<typeof VolumesTab> | null>(null)
  const networksTabRef = ref<InstanceType<typeof NetworksTab> | null>(null)

  /** 刷新当前 tab 的清单（未挂载的 tab 无需刷新 —— lazy 首挂时天然是第一拉）。 */
  function refreshActive() {
    if (activeTab.value === 'images') void imagesTabRef.value?.refresh()
    else if (activeTab.value === 'volumes') void volumesTabRef.value?.refresh()
    else void networksTabRef.value?.refresh()
  }

  function refreshAll() {
    // 主机清单可能已变化（新主机入库），与当前 tab 的清单一起重拉。
    void loadHosts()
    refreshActive()
  }
</script>

<style lang="scss" scoped>
  /* 页面骨架（hero/三态/动效降级）下沉在 views/wkl-shell.scss（本模块多页共用的
   * 范式样式）；这里只留本页特有的结构。 */
  @use './wkl-shell';

  /* 次要文字对比度 AA（P2 打磨批，与总览页同款处置）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1（QA 实测 2.97–3.08），低于
     AA 正文线 → 页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。
     只重定义变量值，三个 tab（含各自表头/合计行）一起达标，不碰元素样式与布局。 */
  .docker-resources-page {
    --el-text-color-secondary: var(--el-text-color-regular);

    /* hero 副标题/g-600 辅助字的对比度 AA（终审 QA D2·浅色实测 3.5:1）：
       text-g-600 的工具变量指向 --art-gray-600(#7987a1) —— 对页底 #fafbfc 3.5:1，
       低于 AA 正文线。页面范围内抬一档到 g-700（浅色 #4d5875 对页底 ≈6.8:1；
       暗色 #ababba 对暗底 ≈8.9:1，随主题自适应）。只重定义工具变量值，页面内
       所有 text-g-600 文字（hero 副标题、加载提示）一起达标，页面外无副作用。 */
    --color-g-600: var(--art-gray-700);
  }

  // tab 导航：紧贴 hero 之下，三个 tab 的图标沿用被收敛的三条旧菜单的图标
  // （box/硬盘/转发）—— 侧边栏里消失的视觉词汇在页面内延续。
  .docker-resources-tabs {
    // ElTabs 默认头距内容 15px；对齐模块节奏（12px，与 hero 的下边距同一拍）。
    :deep(.el-tabs__header) {
      margin: 0 0 12px;
    }

    // 激活态字重加强「在哪里」：颜色已由主题色表达，字重补足扫读锚点。
    :deep(.el-tabs__item.is-active) {
      font-weight: 600;
    }
  }

  // tab 标签（图标 + 文案）：图标继承字号、与文字居中对齐。
  .docker-resources-tab-label {
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }
</style>
