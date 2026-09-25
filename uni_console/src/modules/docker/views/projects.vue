<template>
  <DockerPage
    :loading="loading"
    :stale="state?.stale ?? false"
    :age-seconds="state?.ageSeconds ?? 0"
    :never-reported="state?.neverReported ?? false"
    @refresh="loadState"
  >
    <template #table>
      <!-- 提示行：只陈述当前能力边界，不写「敬请期待」这类空话。
           这句话也解释了项目页与容器页的口径差：非 compose 管理的裸容器不在本页。 -->
      <div class="docker-hint">本页可查看项目配置；编辑与新增网元在后续版本提供</div>

      <ArtTableHeader :loading="loading" @refresh="loadState" />

      <!-- 两种空态分开：主机上没有项目 vs 清单还没到（后者尚不知有没有主机，
           不能先喊「没有项目」），故 v-if 把主机清单的加载态一并算进来。
           空态渲染在本页、不写进 ArtTable 的 `#empty` 插槽：ArtTable 不转发该插槽
           （内部把 ElTable 的空态写死成「暂无数据」），写进去会被静默丢弃。 -->
      <ArtTable v-if="showTable" :loading="listLoading" :data="projects" :columns="columns" />
      <!-- host-context.ts 的 reload 注释承诺：清单拉不到时页面显示「没有可管理的主机」 -->
      <ElEmpty v-else-if="!ctx.hosts.length" class="docker-empty" description="没有可管理的主机" />
      <ElEmpty v-else class="docker-empty" description="该主机上还没有项目" />
    </template>
  </DockerPage>

  <!-- 配置查看器：一期**只读**（无编辑入口 —— 编辑是四期能力，spec §11.0）。
       失败原因直接显示服务端/agent 给的结论句：这里再包一层「操作失败」只会把
       「设备离线」「路径没记录」「文件被删」说成同一句话。 -->
  <ElDialog v-model="configDialog.visible" :title="`配置 · ${configDialog.project}`" width="760px">
    <div v-loading="configDialog.loading" class="docker-yml">
      <ElAlert
        v-if="configDialog.error"
        type="warning"
        :title="configDialog.error"
        :closable="false"
      />
      <pre v-else class="docker-yml__body">{{ configDialog.content }}</pre>
    </div>
    <template #footer>
      <ElButton @click="configDialog.visible = false">关闭</ElButton>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  import { computed, h, ref, watch } from 'vue'
  import { ElAlert, ElButton, ElDialog, ElEmpty } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerInspect } from '@/enums/permission'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import ArtTableHeader from '@/components/core/tables/art-table-header/index.vue'
  import DockerPage from '../components/docker-page.vue'
  import {
    fetchDockerCmdResult,
    fetchDockerState,
    sendDockerCmd,
    type DockerProjectItem,
    type DockerStateResp
  } from '../api'
  import { pollDelay } from '../utils/cmd'
  import { provideDockerHost } from '../utils/host-context'

  // 主机上下文是**页面级** provide/inject：DockerPage 与 HostSwitcher 都用 useDockerHost() 取它，
  // 而模块里没有别的 provide 调用方 —— 页面就是这一层的提供者，故在这里 provide 并直接用其返回值。
  // 不要解构：上下文字段是 getter，解构会把 hostId 定格成进入页面时的 ''（主机清单尚未到达）。
  const ctx = provideDockerHost()
  const { hasAuth } = useAuth()

  const loading = ref(false)
  const state = ref<DockerStateResp | null>(null)
  /** 请求序号：只有最新一次请求的响应能落盘（见 loadState 的备注）。 */
  let loadSeq = 0

  /** 配置对话框的状态（一个对象而不是四个 ref：它们总是同时被写，分开只会漏更新）。 */
  const configDialog = ref({
    visible: false,
    project: '',
    loading: false,
    content: '',
    error: '',
    hash: ''
  })

  /** 项目清单不经筛选（一期不在项目页做搜索），故直接用快照里的顺序。 */
  const projects = computed(() => state.value?.projects ?? [])

  /** 表格的加载态：快照在拉，或主机清单还没到（后者尚不知有没有主机，不能先喊「没有项目」）。 */
  const listLoading = computed(() => loading.value || ctx.loading)

  /** 有行就渲染表格；纯加载中也用表格的 loading 遮罩，空态只在加载结束后判断。 */
  const showTable = computed(() => listLoading.value || projects.value.length > 0)

  /** 项目状态：后端由成员容器运行态归纳出 running/partial/stopped 三态（未知给「—」）。 */
  function projectStateText(stateValue?: string): string {
    if (stateValue === 'running') return '运行中'
    if (stateValue === 'partial') return '部分运行'
    if (stateValue === 'stopped') return '已停止'
    return '—'
  }

  /** 取路径的 basename（列里只放文件名，全路径放悬浮）。 */
  function baseName(path: string): string {
    const parts = path.split('/')
    return parts[parts.length - 1] || path
  }

  const columns = computed(() => [
    { type: 'index' as const, width: 60, label: '序号' },
    { prop: 'name', label: '项目名', minWidth: 180, showOverflowTooltip: true },
    {
      prop: 'state',
      label: '状态',
      width: 110,
      formatter: (row: DockerProjectItem) => projectStateText(row.state)
    },
    { prop: 'services', label: '网元数', width: 90 },
    { prop: 'containersCount', label: '容器数', width: 90 },
    {
      prop: 'configFiles',
      label: '配置文件',
      minWidth: 260,
      // 列里放 basename（路径常很长），全路径挂在 title 上供悬浮核对；
      // 旧版 compose 不上报路径时把「未知」的原因一并说出来，而不是留一个空格子。
      formatter: (row: DockerProjectItem) => {
        const full = row.configFiles?.[0]
        if (!full) return '未知（旧版 compose 未记录）'
        return h('span', { title: full }, baseName(full))
      }
    },
    {
      prop: 'operation',
      label: '操作',
      width: 110,
      fixed: 'right' as const,
      formatter: (row: DockerProjectItem) =>
        // 一期唯一的项目级动作就是「看配置」（compose.file:read 需要 docker:inspect）。
        // 无权限时不渲染操作按钮：空操作列会让人以为「有东西没加载出来」
        //（与容器/镜像页同一纪律）；编辑入口是四期能力，一期连它的存在都不该暗示。
        hasAuth(PermDockerInspect)
          ? h('div', { class: 'flex items-center' }, [
              h(ArtButtonTable, {
                type: 'view',
                title: '查看配置',
                onClick: () => viewComposeFile(row)
              })
            ])
          : null
    }
  ])

  /** 「查看配置」：受理 → 轮询 → 展示（一期唯一的项目级动作）。 */
  async function viewComposeFile(project: DockerProjectItem) {
    configDialog.value = {
      visible: true,
      project: project.name,
      loading: true,
      content: '',
      error: '',
      hash: ''
    }
    try {
      const { ref: cmdRef } = await sendDockerCmd(ctx.hostId, {
        action: 'compose.file:read',
        target: project.name
      })
      let attempt = 0
      for (;;) {
        const res = await fetchDockerCmdResult(ctx.hostId, cmdRef)
        if (res.status !== 'pending') {
          if (res.status === 'succeeded') {
            const p = res.payload as { content?: string; hash?: string } | undefined
            configDialog.value.content = p?.content ?? ''
            configDialog.value.hash = p?.hash ?? ''
            if (!configDialog.value.content) configDialog.value.error = '未取到配置文件内容'
          } else {
            // 失败原因由服务端/agent 给的**结论句**承载（不再包一层「操作失败」）。
            configDialog.value.error = res.error || '读取配置文件失败'
          }
          break
        }
        await new Promise((r) => setTimeout(r, pollDelay(attempt++)))
        if (attempt > 20) {
          configDialog.value.error = '读取超时，请稍后重试'
          break
        }
      }
    } catch {
      // 受理期失败（403/409/503）：设备可能已离线，或权限不足 —— 措辞停在能确定的边界上。
      configDialog.value.error = '指令未受理（设备可能已离线）'
    } finally {
      configDialog.value.loading = false
    }
  }

  /**
   * 拉一次快照。静默失败：陈旧/离线由页面头部标注，不弹错（与设备页同一取向）。
   *
   * seq 守卫（照 system-monitor/views/server.vue 的既有模式）：主机切换会并发两份请求，
   * 旧主机那份可能**晚于**新主机返回 —— 直接落盘会把列表换回上一台机器的项目。
   * 故递增序号，回头发现已被更新的请求取代就丢弃（loading 也由最新那次收尾）。
   */
  async function loadState() {
    if (!ctx.hostId) return
    const seq = ++loadSeq
    loading.value = true
    try {
      const res = await fetchDockerState(ctx.hostId)
      if (seq !== loadSeq) return
      state.value = res
    } catch {
      if (seq !== loadSeq) return
      state.value = null
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  // 主机切换 = 换一台机器：关掉对话框（它属于上一台主机）并重新拉快照。
  watch(
    () => ctx.hostId,
    () => {
      configDialog.value.visible = false
      void loadState()
    }
  )

  // 首次进入：先拉主机清单（query 里的主机写回/落到第一台），再拉快照。
  // 若 hostId 要等清单到达才从 '' 变成首台 id，上面的 watch 会补一次。
  void ctx.reload().then(loadState)
</script>

<style lang="scss" scoped>
  // 空态渲染在本页（ArtTable 不转发 `#empty`）：给它接近表格空态的留白。
  .docker-empty {
    padding: 56px 0;
  }

  .docker-hint {
    margin-bottom: 12px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  // 配置文本：等宽字体 + 保留原始换行；长行折行（yml 里可能有很长的 environment）。
  .docker-yml {
    max-height: 60vh;
    overflow: auto;

    &__body {
      margin: 0;
      font-family: var(--art-font-family-mono, monospace);
      font-size: 12px;
      line-height: 1.6;
      white-space: pre-wrap;
      word-break: break-all;
      color: var(--el-text-color-primary);
    }
  }
</style>
