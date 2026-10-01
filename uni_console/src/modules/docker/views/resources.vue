<template>
  <!-- ⚠ 单根包装：页面**必须只有一个根节点**（布局把页面放进 `<Transition mode="out-in">`，
       而 Transition 只支持单根元素）。三个 tab 的确认弹窗/对话框随各自的 tab 组件
       收在它自己的单根里，页面级只剩 DockerPage 一个根（single-root.test.ts 扫描钉住）。 -->
  <div class="docker-resources-page">
    <DockerPage
      :loading="loading"
      :stale="stale"
      :age-seconds="ageSeconds"
      :never-reported="neverReported"
      :load-error="loadError"
      :has-state="hasState"
      @refresh="refresh"
    >
      <!-- 内容区走 DockerPage 的**默认插槽**（7a 新增的内容形态）：三个 tab 各自带
           搜索栏与表格卡片，DockerPage 不再替它们包 ElCard（卡片套卡片）。
           主机条（切换器 + 同步文案 + 刷新）只有这一条 —— 三个 tab 共享同一台主机，
           这是收敛的核心收益：切 tab 不换主机、不重拉快照。 -->
      <ElTabs v-model="activeTab" class="docker-resources-tabs">
        <ElTabPane name="images" lazy>
          <template #label>
            <span class="docker-resources-tab-label">
              <ArtSvgIcon icon="ri:box-1-line" />
              镜像
            </span>
          </template>
          <ImagesTab ref="imagesTabRef" :state="state" :loading="loading" :refresh="refresh" />
        </ElTabPane>
        <ElTabPane name="volumes" lazy>
          <template #label>
            <span class="docker-resources-tab-label">
              <ArtSvgIcon icon="ri:hard-drive-3-line" />
              数据卷
            </span>
          </template>
          <VolumesTab ref="volumesTabRef" :state="state" :loading="loading" :refresh="refresh" />
        </ElTabPane>
        <ElTabPane name="networks" lazy>
          <template #label>
            <span class="docker-resources-tab-label">
              <ArtSvgIcon icon="ri:share-forward-line" />
              网络
            </span>
          </template>
          <NetworksTab ref="networksTabRef" :state="state" :loading="loading" :refresh="refresh" />
        </ElTabPane>
      </ElTabs>
    </DockerPage>
  </div>
</template>

<script setup lang="ts">
  /**
   * 镜像与存储页（7a 旧页收敛）：镜像 / 数据卷 / 网络三张旧列表页收敛为一个
   * tab 容器页。
   *
   * 页面职责只有三件事（编排，不含业务）：
   *   1. **页面级一个主机上下文**：provideDockerHost() 在这里提供，三 tab 经
   *      useDockerHost() 注入共享 —— 切 tab 不换主机（旧三页各自一份上下文，
   *      换 tab 等于换页，主机要重选一次）。
   *   2. **一份快照**：useDockerHostState 只在本页调用一次，state/loading/refresh
   *      作为 props 下发给三个 tab（快照本就是整份的：images/volumes/networks
   *      同源）。切 tab 不重拉；写指令成功后的重拉（useDockerCmds 的双次重拉）
   *      也只拉这一份。
   *   3. **当前 tab 记在 URL query**（?tab=images|volumes|networks）：刷新与分享
   *      链接都能还原现场；总览磁盘面板、镜像详情返回链路用 query.host + query.tab
   *      直接落到「那台主机的那张表」。
   *
   * 主机切换的重置纪律（原三页各自 onHostSwitch 的正文）**合并到页面级一份**：
   * onHostSwitch 里逐个调用已挂载 tab 的 resetForHostSwitch（清筛选/清勾选/关拉取
   * 对话框 —— 逐项平移，见各 tab 组件）。未挂载的 tab（lazy：还没访问过）没有
   * 任何状态需要重置，首次挂载天然是干净的。
   */
  import { ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import { ElTabPane, ElTabs } from 'element-plus'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import DockerPage from '../components/docker-page.vue'
  import ImagesTab from '../components/resources/images-tab.vue'
  import VolumesTab from '../components/resources/volumes-tab.vue'
  import NetworksTab from '../components/resources/networks-tab.vue'
  import { useDockerHostState } from '../composables/useDockerHostState'
  import { provideDockerHost } from '../utils/host-context'

  // 主机上下文是**页面级** provide/inject：DockerPage 与各 tab 都用 useDockerHost()
  // 取它 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  provideDockerHost()

  const route = useRoute()
  const router = useRouter()

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
  // tab → query：用户点 tab 时写回（replace 不进历史 —— tab 切换不该占用后退键，
  // 与主机切换的 selectHost 同一取向）。
  watch(activeTab, (tab) => {
    if (String(route.query.tab ?? '') !== tab) {
      void router.replace({ query: { ...route.query, tab } })
    }
  })

  // ── 页面级一份快照 + 主机切换重置（三 tab 的纪律合并处）──────────────────

  const imagesTabRef = ref<InstanceType<typeof ImagesTab> | null>(null)
  const volumesTabRef = ref<InstanceType<typeof VolumesTab> | null>(null)
  const networksTabRef = ref<InstanceType<typeof NetworksTab> | null>(null)

  const { state, loading, stale, ageSeconds, neverReported, loadError, hasState, refresh } =
    useDockerHostState({
      // 主机切换 = 换一台机器：原三页各自的重置纪律合并成这一份（清空各 tab 的
      // 筛选/勾选，关掉镜像 tab 的拉取进度对话框 —— 一场拉取属于受理它的那台主机）。
      onHostSwitch: () => {
        imagesTabRef.value?.resetForHostSwitch()
        volumesTabRef.value?.resetForHostSwitch()
        networksTabRef.value?.resetForHostSwitch()
      }
    })
</script>

<style lang="scss" scoped>
  // tab 导航是本页唯一新增的结构元素（收敛的入口形态）：紧贴主机条之下，
  // 三个 tab 的图标沿用被收敛的三条旧菜单的图标（box/硬盘/转发）—— 侧边栏里
  // 消失的视觉词汇在页面内延续，用户按形状就能找到原来的那张表。
  .docker-resources-tabs {
    // ElTabs 默认头距内容 15px；对齐模块节奏（12px，与 docker-page__bar 同一拍）。
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
