<template>
  <!-- 单根（single-root 守卫扫描全模块的 .vue）：与 image-detail 的其它 Tab 内容
       同为「一个语义块一个根」的挂法。 -->
  <div class="isc">
    <!-- ── 扫描在途：加载态 + 「去哪看进度」的提示 ──
         没有进度流（协议口径：trivy 的 json 模式结束才出完整报告，中途没有可增量
         的结构化进度），pending 期间在任务中心的可见性就是「扫了没反应」的答案；
         首扫还要下载漏洞库，几分钟是常态 —— 不说清这段时间，加载态会被读成卡死。 -->
    <template v-if="scanPhase === 'scanning'">
      <ElSkeleton :rows="6" animated />
      <p class="isc-note">
        正在扫描，可能需要几分钟（首次扫描还要先下载漏洞库）；进度可在任务中心查看。
      </p>
    </template>

    <template v-else>
      <!-- ── 空态（还没在本页展示过报告）：引导按钮 ──
           按钮发一条 image:scan 指令：没扫过 = 真扫描（分钟级）；24 小时内扫过 =
           服务端缓存秒回（交互形状相同，差别只是立刻出报告）。 -->
      <template v-if="!report">
        <ElEmpty v-if="scanPhase !== 'failed'" description="这个镜像还没有安全报告">
          <ElButton v-if="canManage" size="small" type="primary" @click="startScan">
            扫描镜像
          </ElButton>
        </ElEmpty>
        <!-- 失败（含未装 trivy 的结论句原文）：agent 给的结论句就是这一屏该说的话，
             原样透传 + 重试入口（装好 trivy 后重试即可用）。 -->
        <ElEmpty v-else :description="errorText">
          <ElButton v-if="canManage" size="small" @click="startScan">重试</ElButton>
        </ElEmpty>
        <p v-if="canManage && scanPhase !== 'failed'" class="isc-note"
          >报告按镜像内容缓存 24 小时：已扫过的镜像再点会直接显示最近一次结果。</p
        >
      </template>

      <!-- ── 报告态：计数行 + CVE 表 ── -->
      <template v-else>
        <!-- 重新扫描失败不抹掉旧报告（旧结论仍有效，错的是这一次重试）：
             结论降为警示行，与报告并列。 -->
        <p v-if="scanPhase === 'failed'" class="isc-warn">{{ errorText }}</p>

        <div class="isc-head">
          <span class="isc-head__total">共 {{ total }} 条</span>
          <span class="isc-head__time">扫描于 {{ scannedText }}</span>
          <!-- 重新扫描：此分支只在非扫描态渲染（扫描态走顶部的加载分支），无需禁用。 -->
          <ElButton v-if="canManage" size="small" @click="startScan"> 重新扫描 </ElButton>
        </div>

        <!-- 五档计数（全量口径 —— 不受 500 条截断影响）：点是色、数是行动依据
             （严重/高危要不要立刻处理）。零档也展示：行形稳定，且「都是 0」
             本身就是这个镜像的安全结论。 -->
        <div class="isc-counts">
          <span v-for="s in SCAN_SEVERITIES" :key="s.key" class="isc-chip">
            <span class="isc-dot" :class="scanSeverityClass(s.key)"></span>
            <span class="isc-chip__label">{{ s.label }}</span>
            <span class="isc-chip__num">{{ report.counts[s.key] }}</span>
          </span>
        </div>

        <!-- 截断披露：计数是全量、条目只有前 500 —— 不说清，合计与行数对不上
             会被当成数据错误。 -->
        <p v-if="report.truncated" class="isc-truncated"
          >条目较多，仅显示前 {{ MAX_SCAN_VULN_ENTRIES }} 条（共
          {{ total }} 条，计数为全部结果）。</p
        >

        <ArtTable v-if="report.vulns.length" :data="rows" :columns="columns" />
        <ElEmpty v-else description="没有发现漏洞" />
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
  /**
   * 镜像安全扫描面板（P3·安全面前端半边）：镜像详情页「安全」Tab 的内容体。
   *
   * 通道与 image:inspect 同族（受理 image:scan 指令 → 轮询 result → 读 payload），
   * 但**不走** 30 秒档的只读闭环：真扫描要跑数分钟（trivy 首扫还要下载漏洞库），
   * 这里的轮询上限单独放宽到覆盖 agent 的 15 分钟执行档；进行中的可见性交给
   * 任务中心（指令在 pending 期间本就出现在那里），本面板只承担「触发 + 呈现」。
   *
   * 缓存语义（服务端 24h、按镜像内容键）：命中时同一条指令秒回最近一次的报告
   * （scanned_at 由服务端重盖）—— 「扫描于 N 小时前」在缓存回放路径上如实成立，
   * 前端不需要（也无法）区分这条报告是刚扫的还是回放的。
   *
   * 为什么是独立组件而不是详情页内联：三个 Tab 以外再塞一张状态机（空/扫描中/
   * 失败/报告 × 触发/重试）会把页面顶到千行；面板自持状态机、页面只出入两份
   * 事实（主机 + 目标引用）—— 与推送/创建入口在详情页的挂法同一取舍。
   */
  import { computed, h, ref } from 'vue'
  import { ElButton, ElEmpty, ElSkeleton } from 'element-plus'
  import { useAuth } from '@/hooks/core/useAuth'
  import { PermDockerManage } from '@/enums/permission'
  import ArtTable from '@/components/core/tables/art-table/index.vue'
  import { formatRelativeTime } from '../utils/display'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import {
    MAX_SCAN_VULN_ENTRIES,
    SCAN_SEVERITIES,
    parseDockerScanReport,
    scanSeverityClass,
    scanSeverityLabel,
    scanTotalCount,
    type DockerScanReportView
  } from '../utils/scan'

  defineOptions({ name: 'DockerImageScanPanel' })

  const props = defineProps<{
    /** 目标主机（受理时由 useDockerCmds 钉死 —— 扫描只属于受理它的那台主机）。 */
    hostId: string
    /** 被扫镜像引用（仓库标签优先，无标签退回镜像 id —— 与详情页写操作的口径同源）。 */
    target: string
  }>()

  const { hasAuth } = useAuth()
  const canManage = computed(() => hasAuth(PermDockerManage))

  type ScanPhase = 'idle' | 'scanning' | 'failed'

  const scanPhase = ref<ScanPhase>('idle')
  const report = ref<DockerScanReportView | null>(null)
  /** 失败结论句（agent/服务端原文优先）：无旧报告时是空态的错误描述；有旧报告
      时降为警示行（重新扫描失败不抹掉仍有效的旧结论）。 */
  const errorText = ref('')

  /**
   * 轮询上限：约 15.3 分钟（pollDelay 1s→2s→4s→5s 封顶，7 + 5×(N-3) 秒）。
   *
   * 覆盖 agent 的 15 分钟执行档（trivy 自身 --timeout 14 分钟更短，会先带原因
   * 失败）；useDockerCmds 的默认 20 次（约 92 秒）是给秒级指令的，对扫描来说
   * 会在第 92 秒谎报「操作超时」而扫描其实还在跑。
   */
  const MAX_SCAN_POLLS = 185

  // 扫描的指令通道：不接 refresh（扫描不改任何快照事实，成功后无需重拉）。
  const { run } = useDockerCmds({ hostId: () => props.hostId, maxPollAttempts: MAX_SCAN_POLLS })

  /** 漏洞总数（severity 全量计数求和 —— 截断时它与表格行数不同，这正是要披露的）。 */
  const total = computed(() => (report.value ? scanTotalCount(report.value.counts) : 0))
  const scannedText = computed(() =>
    report.value ? formatRelativeTime(report.value.scannedAt) : ''
  )

  /** 表格行（视图直出 —— 条目本体已是展示形态，行映射只做空值兜底）。 */
  const rows = computed(() =>
    (report.value?.vulns ?? []).map((v, i) => ({
      key: i,
      id: v.id || '—',
      pkg: v.pkg || '—',
      severity: v.severity,
      fixed: v.fixedVersion ?? '',
      title: v.title ?? ''
    }))
  )

  /**
   * 严重度单元格（点 + 档名）：h() 造的节点拿不到 scoped 属性，样式经
   * :deep(.isc-sev) 穿透（与详情页「指令」列的等宽处理同一手法）。
   */
  function severityCell(row: { severity: string }) {
    return h('span', { class: 'isc-sev' }, [
      h('span', { class: `isc-dot ${scanSeverityClass(row.severity)}` }),
      h('span', { class: 'isc-sev__label' }, scanSeverityLabel(row.severity))
    ])
  }

  /** 修复版本：等宽（版本号是标识符）；空 = 修复未发布/不适用，如实说「无修复」。 */
  function fixedCell(row: { fixed: string }) {
    return row.fixed
      ? h('code', { class: 'isc-mono' }, row.fixed)
      : h('span', { class: 'isc-sev__none' }, '无修复')
  }

  const columns = [
    {
      prop: 'id',
      label: '漏洞编号',
      minWidth: 170,
      showOverflowTooltip: true,
      formatter: (row: { id: string }) => h('code', { class: 'isc-mono' }, row.id)
    },
    { prop: 'pkg', label: '受影响包', minWidth: 160, showOverflowTooltip: true },
    { prop: 'severity', label: '严重度', minWidth: 96, formatter: severityCell },
    { prop: 'fixed', label: '修复版本', minWidth: 120, formatter: fixedCell },
    { prop: 'title', label: '摘要', minWidth: 260, showOverflowTooltip: true }
  ]

  /** 触发扫描（空态引导/失败重试/报告态重新扫描共用一条路径）。 */
  async function startScan(): Promise<void> {
    if (scanPhase.value === 'scanning') return
    if (!props.hostId || !props.target) return // 主机/目标未落定（路由还在过渡）
    scanPhase.value = 'scanning'
    errorText.value = ''
    const res = await run({ action: 'image:scan', target: props.target })
    if (res.ok) {
      const parsed = parseDockerScanReport(res.payload)
      if (parsed) {
        report.value = parsed
        scanPhase.value = 'idle'
        return
      }
      // 成功却读不出报告：结论如实（不编「0 条」—— 那会把「没读到」伪装成「干净」，
      // 安全面最不可接受的失败形态，与 agent 对 trivy 缺席的态度同一条纪律）。
      scanPhase.value = 'failed'
      errorText.value = '扫描已完成，但报告未能读取，请重试'
      return
    }
    scanPhase.value = 'failed'
    // 失败结论句原文（agent 的「未装 trivy」/服务端的「在执行中」都靠它说清）。
    errorText.value = runErrorMessage(res, '扫描未完成')
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  // 空态/加载态下的说明行：结论弱化（它解释「为什么慢/为什么没有」，不是数据）。
  .isc-note {
    margin: 8px 0 0;
    font-size: 12px;
    line-height: 1.7;
    color: var(--el-text-color-secondary);
    text-align: center;
  }

  // 重新扫描失败（旧报告仍在）：警示行与报告并列，不顶替。
  .isc-warn {
    margin: 0 0 8px;
    padding: 6px 10px;
    font-size: 12px;
    line-height: 1.6;
    border-radius: 4px;
    color: var(--el-color-warning);
    background: var(--el-color-warning-light-9);
    word-break: break-all;
  }

  // 报告头：总数（主信息）+ 扫描时刻（元数据，弱化）+ 重新扫描（动作就近）。
  .isc-head {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 16px;
    align-items: center;
    margin-bottom: 10px;

    &__total {
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__time {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }

  // 五档计数行：chip = 色点 + 档名 + 计数。点的颜色按档映射 EP 语义色
  //（严重 = danger 红；高危 = warning 橙；中危 = warning 的浅档 —— EP 没有独立
  // 的橙/黄两个语义色，同一 warning 色相用深浅分档；低危 = primary 蓝；
  // 未知 = info 灰）。零档保留：行形稳定，「全零」本身就是结论。
  .isc-counts {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 20px;
    margin-bottom: 10px;
  }

  .isc-chip {
    display: inline-flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;

    &__label {
      color: var(--el-text-color-regular);
    }

    &__num {
      font-weight: 600;
      color: var(--el-text-color-primary);
    }
  }

  .isc-dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;

    &.is-critical {
      background: var(--el-color-danger);
    }

    &.is-high {
      background: var(--el-color-warning);
    }

    &.is-medium {
      background: var(--el-color-warning-light-3);
    }

    &.is-low {
      background: var(--el-color-primary);
    }

    &.is-unknown {
      background: var(--el-color-info);
    }
  }

  // 截断披露：口径说明弱化（数字在计数行，这句话只解释「为什么行数对不上」）。
  .isc-truncated {
    margin: 0 0 8px;
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-text-color-secondary);
  }

  // h() 造的单元格节点拿不到 scoped 属性（与详情页 :deep(.imd-mono) 同手法）。
  :deep(.isc-sev) {
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }

  :deep(.isc-mono) {
    font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
  }

  :deep(.isc-sev__none) {
    color: var(--el-text-color-secondary);
  }
</style>
