<template>
  <!-- 单根包装（single-root 守卫扫全模块 .vue）：批量栏与确认弹窗是兄弟节点。 -->
  <div class="wkl-batch-root">
    <!-- 吸底批量区（spec §11.1）：勾选栏 + 波次反馈收在同一块里，选中/批量期间贴在
         表卡可视区底缘（sticky）—— 行数多时原「留在文档流尾部」的入口会被挤到可视
         区外（实拍选中后入口被裁 / 完全不可见）。勾选栏只有选中时才有意义；波次反馈
         不依赖勾选（批量期间列表重拉会让勾选闪空），故本块的 v-if 是二者的并集。
         `bottomOffset`：本波引入分页器后，分页器渲染在表内底部（本块之后）；把吸底块
         的 sticky bottom 抬到分页器上缘（= 分页器高度 + 间距）即与之不重叠。零值
         （未传 / 无分页器）时退回 CSS 的 bottom: 0，行为与 W1 完全一致。 -->
    <div
      v-if="selected.length || waveCurrent || waveNotes.length"
      ref="barEl"
      class="wkl-batch"
      :style="offsetPx > 0 ? { bottom: offsetPx + 'px' } : undefined"
    >
      <template v-if="selected.length">
        <span class="wkl-batch__count">已选 {{ selected.length }} 项</span>
        <!-- 动作钮从注册表取（标签/危险度/权限码随动作码而来）：`…` 表示点了要先过
             一道确认弹窗（本波起启停重启也要确认，故一律带 `…`）。 -->
        <ElButton
          v-for="btn in buttons"
          :key="btn.action"
          size="small"
          :type="btn.danger ? 'danger' : undefined"
          :plain="btn.danger"
          :disabled="disabled"
          @click="openDialog(btn.spec)"
        >
          {{ btn.label }}
        </ElButton>
      </template>

      <!-- 波次推进（6d）：跨主机批量按主机分波、逐波推进 —— 当前波给「正在执行 主机 B
           （3/12）」的节奏行，完成的波逐行给波级结论（「主机 A：12/12 成功（web、…）」，
           成功项列名、失败项带原因；拼装见 utils/batch 的 waveConclusion）。
           渲染不依赖勾选：批量期间列表重拉会让勾选闪空（行对象被换掉），反馈行跟着
           勾选消失的话恰好在最该看的时候看不见。下一轮批量开始时清空重记。 -->
      <div v-if="waveCurrent || waveNotes.length" class="wkl-batch__waves">
        <div v-if="waveCurrent" class="wkl-batch__wave is-current">
          正在执行 {{ waveCurrent.host }}…（{{ waveCurrent.done }}/{{ waveCurrent.total }}）
        </div>
        <div
          v-for="(note, i) in waveNotes"
          :key="i"
          class="wkl-batch__wave"
          :class="{ 'is-failed': note.failed }"
        >
          {{ note.text }}
        </div>
      </div>
    </div>

    <!-- 批量动作的确认弹窗（**一只弹窗三档形态**，按注册表的危险度与动作描述推导）：
           - word 档（删除家族，danger/destructive）：列出目标 + 主机，逐字输入 DELETE
             —— 后端单条删除是标准档，但一次动多个目标，前端自加一道 DELETE 摩擦
             （计划 D2 / spec §11.6，纯前端确认，不往协议加字段）；
           - input 档（动作要一个参数，如导出 tar 的文件名前缀）：列出目标 + 参数输入；
           - plain 档（启停重启等常规动作）：列出目标 + 主机，一次确认即可
             —— 批量是「一次动 N 个」的决定，值得先看清动的是谁。
           受保护目标：有 docker:exec 权限时给「强制操作」开关（与单条流的
           action-confirm 同一档），勾选后才给受保护行带 force。 -->
    <ElDialog
      v-model="dialog.visible"
      :title="dialogTitle"
      width="460px"
      :close-on-click-modal="false"
    >
      <div class="wkl-batch-del">
        <div class="wkl-batch-del__line">{{ dialogLine }}</div>
        <ul class="wkl-batch-del__list">
          <li v-for="row in dialog.rows" :key="`${row.hostId}:${row.name}`">
            <span>{{ row.name }}</span>
            <span class="wkl-batch-del__host">@ {{ row.hostname }}</span>
            <!-- 受保护行标锁（锁图标 + 受保护）：不用 🔒 emoji —— 无 emoji 字体的
                 环境里会渲染成豆腐块；锁走 ArtSvgIcon 的图标范式。 -->
            <span v-if="row.protected" class="wkl-batch-del__lock">
              <ArtSvgIcon icon="ri:lock-2-line" />
              受保护
            </span>
          </li>
        </ul>

        <!-- input 档：先收集参数（如文件名前缀），提示讲清命名规则与产物落点。 -->
        <template v-if="dialog.kind === 'input'">
          <div class="wkl-batch-del__label">{{ dialog.inputLabel }}</div>
          <ElInput
            v-model="dialog.input"
            class="wkl-batch-del__input"
            :placeholder="dialog.inputPlaceholder"
            @keyup.enter="confirmDialog"
          />
          <div v-if="dialog.inputHint" class="wkl-batch-del__hint">{{ dialog.inputHint }}</div>
        </template>

        <ElCheckbox v-if="showForce" v-model="dialog.force" class="wkl-batch-del__force">
          强制操作
        </ElCheckbox>

        <template v-if="dialog.kind === 'word'">
          <div class="wkl-batch-del__line">此操作不可恢复。输入 DELETE 以确认：</div>
          <ElInput
            v-model="dialog.input"
            class="wkl-batch-del__input"
            placeholder="DELETE"
            @keyup.enter="confirmDialog"
          />
        </template>
      </div>
      <template #footer>
        <ElButton @click="dialog.visible = false">取消</ElButton>
        <ElButton :type="dialog.danger ? 'danger' : 'primary'" :disabled="!canConfirm" @click="confirmDialog">
          {{ dialog.confirmText }}
        </ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  /**
   * 跨主机统一表的批量栏（**资源无关**：容器/镜像/数据卷/网络四页共用）。
   *
   * 与容器页最早那版（写死 container:start/stop/restart/remove 四个钮）的差别：
   *   - 动作集由页面经 `actions` 传入（BatchActionSpec，动作码取 utils/actions 注册表），
   *     按钮标签/危险度/权限门控全部从注册表推导 —— 栏不知道自己动的是哪种资源；
   *   - 选中行由页面**投影**成 BatchRow（名字/主机/受保护/页面侧不发送原因），
   *     条目类型（四个不同 DTO）不进本组件；
   *   - 确认档按动作推导：删除家族走逐字 DELETE（比单条更强，与容器页同强度），
   *     其余动作走一次普通确认（列出目标 + 主机）；需要参数的动作用 input 档收集。
   *
   * 与旧单主机页的唯一语义差别：**跨主机批量 = 逐行按行主机派发**。旧页守卫
   * 「主机一换剩余不发」的前提（页面有单一当前主机）在统一表里不存在了 ——
   * 每行自带归属，循环里每次派发前把指令通道的 hostId 换到该行（useDockerCmds
   * 在 run 入口处对 hostId 求值，换 ref 即生效）。失败收集口径照旧。
   *
   * 6d 批量波次：派发从「平铺逐行」改为**按主机分波、逐波推进**（同主机归一波，
   * 下一波等上一波落定）—— 跨主机批量的部分进度从此按主机可读（波级结论行），
   * 波内逐行照旧串行、失败收集照旧跨全批累计批末汇总（语义边界见 utils/batch.ts
   * 的文件头，分组与结论句的纯逻辑也在那里）。
   */
  import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
  import { ElButton, ElCheckbox, ElDialog, ElInput, ElMessage } from 'element-plus'
  import { PermDockerExec } from '@/enums/permission'
  import { useAuth } from '@/hooks/core/useAuth'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import type { DockerWorkloadItem } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import {
    groupBatchWaves,
    waveConclusion,
    type BatchFailure,
    type BatchWave
  } from '../utils/batch'
  import { lookupDockerAction, protectedGate } from '../utils/actions'
  import {
    CONTAINER_BATCH_ACTIONS,
    measureBatchOffset,
    stickyScrollContainer,
    type BatchActionSpec,
    type BatchRow
  } from './batch-actions'

  defineOptions({ name: 'DockerWorkloadBatchBar' })

  const props = withDefaults(
    defineProps<{
      /** 当前勾选的行（页面持有并投影，表格 selection-change 冒泡上来）。 */
      selected: BatchRow[]
      /** 资源名词（容器/镜像/数据卷/网络）：确认弹窗里指代选中集的词。 */
      noun?: string
      /** 批量动作集（动作码取 utils/actions 注册表）；缺省 = 容器页动作集。 */
      actions?: BatchActionSpec[]
      /** 外部在途（页面单行指令通道的 busy）：批量与单行不并发提交。 */
      busy?: boolean
      /** 成功后的列表重拉（useDockerCmds 的 refresh）。 */
      refresh?: () => void | Promise<void>
    }>(),
    {
      noun: '容器',
      actions: () => CONTAINER_BATCH_ACTIONS,
      busy: false,
      refresh: undefined
    }
  )

  const emit = defineEmits<{
    /** 批量执行在途（起/止各发一次）：页面据此禁用行菜单，防并发提交同一目标。 */
    (e: 'busy-change', value: boolean): void
  }>()

  const { hasAuth } = useAuth()
  const canExec = computed(() => hasAuth(PermDockerExec))

  /** 栏上的动作钮：标签/危险度/权限全从注册表推导；未登记的动作不生成按钮。 */
  const buttons = computed(() =>
    props.actions
      .map((spec) => {
        const entry = lookupDockerAction(spec.action)
        if (!entry) return null
        return {
          spec,
          action: spec.action,
          label: `${entry.label}…`,
          danger: entry.danger !== 'normal',
          perm: entry.perm
        }
      })
      .filter((b): b is NonNullable<typeof b> => b !== null && hasAuth(b.perm))
  )

  /**
   * 逐行派发的主机来源：循环里每次 run 之前置为该行的 hostId。useDockerCmds 在
   * setup 期把函数定住、在 run 入口处求值 —— 所以这里换 ref 就能切主机。
   */
  const rowHostId = ref('')
  const { run: runCmd, busy: selfBusy } = useDockerCmds({
    hostId: () => rowHostId.value,
    refresh: () => props.refresh?.()
  })

  const batchRunning = ref(false)
  const disabled = computed(() => batchRunning.value || selfBusy.value || props.busy)

  // ── 确认弹窗（三档形态共用一个状态）──────────────────────────────
  type DialogKind = 'word' | 'input' | 'plain'

  interface DialogState {
    visible: boolean
    kind: DialogKind
    danger: boolean
    confirmText: string
    inputLabel: string
    inputPlaceholder: string
    inputHint: string
    inputRequired: boolean
    /** 逐字档的期望值（删除家族恒为 DELETE）；其它档为空串。 */
    word: string
    input: string
    force: boolean
    rows: BatchRow[]
    spec: BatchActionSpec | null
  }

  function emptyDialog(): DialogState {
    return {
      visible: false,
      kind: 'plain',
      danger: false,
      confirmText: '',
      inputLabel: '',
      inputPlaceholder: '',
      inputHint: '',
      inputRequired: false,
      word: '',
      input: '',
      force: false,
      rows: [],
      spec: null
    }
  }

  const dialog = ref<DialogState>(emptyDialog())

  /**
   * 开确认弹窗：形态按「注册表的危险度 → 动作是否要参数」两问推导。
   * 删除家族（danger/destructive）恒走逐字 DELETE —— 批量动多个目标比单条更该摩擦。
   */
  function openDialog(spec: BatchActionSpec) {
    const entry = lookupDockerAction(spec.action)
    if (!entry || props.selected.length === 0) return
    const word = entry.danger !== 'normal'
    const kind: DialogKind = word ? 'word' : spec.input ? 'input' : 'plain'
    dialog.value = {
      ...emptyDialog(),
      visible: true,
      kind,
      danger: entry.danger !== 'normal',
      confirmText: entry.label,
      inputLabel: spec.input?.label ?? '',
      inputPlaceholder: spec.input?.placeholder ?? '',
      inputHint: spec.input?.hint ?? '',
      inputRequired: spec.input?.required ?? false,
      word: word ? 'DELETE' : '',
      rows: [...props.selected],
      spec
    }
  }

  /** 弹窗标题：删除家族固定「删除确认」，其余按动作名。 */
  const dialogTitle = computed(() =>
    dialog.value.kind === 'word' ? '删除确认' : `${dialog.value.confirmText}确认`
  )

  /** 弹窗正文首句：动的是谁（删除家族照旧用「将删除」的既有措辞）。 */
  const dialogLine = computed(() => {
    const n = dialog.value.rows.length
    return dialog.value.kind === 'word'
      ? `将删除以下 ${n} 个${props.noun}：`
      : `将对以下 ${n} 个${props.noun}执行「${dialog.value.confirmText}」：`
  })

  /** 「强制操作」开关：选中集里有受保护目标、该动作吃保护档、且本账号有 docker:exec。 */
  const showForce = computed(() => {
    const entry = dialog.value.spec ? lookupDockerAction(dialog.value.spec.action) : undefined
    return (
      canExec.value &&
      entry?.guarded === true &&
      dialog.value.rows.some((r) => r.protected === true)
    )
  })

  /** 提交门槛：逐字档必须一字不差；input 档照必填判定；plain 档恒可提交。 */
  const canConfirm = computed(() => {
    const d = dialog.value
    if (d.kind === 'word') return d.input === d.word
    if (d.kind === 'input') return !d.inputRequired || d.input.trim() !== ''
    return true
  })

  function confirmDialog() {
    if (!canConfirm.value) return
    const d = dialog.value
    const spec = d.spec
    if (!spec) return
    const rows = d.rows
    const input = d.input.trim()
    const force = d.force
    dialog.value = emptyDialog()
    void dispatch(spec, rows, input, force)
  }

  /** 一条波级反馈（text = 结论句；failed = 该波有无失败，行级配色用）。 */
  interface WaveNote {
    text: string
    failed: boolean
  }

  /** 当前波（null = 不在批量中）：节奏行「正在执行 主机 B…（3/12）」。 */
  const waveCurrent = ref<{ host: string; done: number; total: number } | null>(null)
  /** 已完成波的结论行（批末不清空 —— 总结论 toast 转瞬即逝，行内账目留到下一轮）。 */
  const waveNotes = ref<WaveNote[]>([])

  // ── 吸底块 × 分页器：错位量栏自己量 ────────────────────────────────
  /**
   * 吸底块的 sticky bottom 要抬多少才不盖住分页器：分页器的位置是唯一事实来源，
   * 而错位量取决于**吸底块自己的滚动容器**（sticky 的约束矩形）—— 只有栏知道自己在
   * 哪个容器里，故自量（页面不再传；口径见 batch-actions 的 measureBatchOffset）。
   *
   * 三个时机各量一次：栏出现（勾选/波次，栏的进出会让列表让位）/ 勾选集变化（栏内
   * 波次行的高度不同）/ 容器尺寸变化（断点与卡体高度）。量不出布局（jsdom）→ 0，
   * 退回 CSS 的 bottom: 0（无分页器时与 W1 完全一致）。
   */
  const barEl = ref<HTMLElement>()
  const offsetPx = ref(0)
  let offsetObserver: ResizeObserver | null = null

  function measure() {
    offsetPx.value = measureBatchOffset(stickyScrollContainer(barEl.value))
  }

  onMounted(() => {
    measure()
    const sc = stickyScrollContainer(barEl.value)
    if (sc && typeof ResizeObserver !== 'undefined') {
      offsetObserver = new ResizeObserver(() => measure())
      offsetObserver.observe(sc)
    }
    window.addEventListener('resize', measure)
  })

  onBeforeUnmount(() => {
    offsetObserver?.disconnect()
    offsetObserver = null
    window.removeEventListener('resize', measure)
  })

  watch(
    [() => props.selected.length, () => waveCurrent.value, () => waveNotes.value.length],
    () => nextTick(measure),
    { flush: 'post' }
  )

  /**
   * 批量 = 循环发指令（后端没有批量动作，照计划 D2 前端循环），6d 起按主机分波推进：
   * **同一主机的行归一波、逐波推进**（下一波等上一波全部落定才开始）—— 跨主机的
   * 批量从此有按主机可读的部分进度；波内逐行照旧**串行**（一次一条、轮询到终态再发
   * 下一条 —— 现状语义原样保留，波次只改分组与节奏，不引入波内并发）。
   *
   * 失败收集口径不变：跨全批累计、批末统一汇总（波级结论句点名到目标 —— 成功项
   * 波级列名、失败项含原因逐一可读，见 utils/batch 的 waveConclusion；批末汇总
   * 仍只报前三条原因，两处不重复也不遗漏）；单条失败不中断（含波内与波间 ——
   * 「3 项成功、1 项失败」是可解释的结果，中途放弃会让剩下的项处于说不清的状态）。
   * 固定行键 'batch'：批量期间 pendingId 不属于任何一行，界面用 batchRunning
   * 禁用整条栏防重复提交。
   *
   * 受保护的目标批量默认不发（批量没有为每一行单独勾「强制」的输入面）：直接按
   * 保护档的结论句计入失败，不浪费一条指令，也让原因可读。**勾了弹窗里的「强制
   * 操作」且本账号有 docker:exec 时**，受保护行带 force=true 发出（与单条流同一
   * 档）；页面侧标了 blocked 的行（如使用中的镜像）恒不发，原因句照给。
   */
  async function dispatch(
    spec: BatchActionSpec,
    rows: BatchRow[],
    input: string,
    force: boolean
  ) {
    if (batchRunning.value || rows.length === 0) return
    // 复制一份：批量期间表格的勾选可能随重拉变化（行对象被换掉即清空），正在执行的
    // 这一批不能跟着变。分波对副本做（分组是执行口径，不是勾选口径）。
    const waves = groupBatchWaves([...rows] as unknown as DockerWorkloadItem[])
    batchRunning.value = true
    emit('busy-change', true)
    waveNotes.value = []
    waveCurrent.value = null
    let done = 0
    const failures: BatchFailure[] = []
    try {
      for (const wave of waves) {
        done += await runWave(wave, spec, input, force, failures)
      }
    } finally {
      batchRunning.value = false
      emit('busy-change', false)
      waveCurrent.value = null
    }
    const message = summary(done, failures)
    if (failures.length > 0) ElMessage({ type: 'warning', message, duration: 5000 })
    else ElMessage.success(message)
  }

  /**
   * 推进一波：波内逐行派发（串行、单条失败不中断），行毕更新节奏行，波毕落一条
   * 波级结论。返回该波成功行数（未发送与失败行不算 —— 批末汇总另有明细）。
   * hostId 在每行派发前置为该行的 hostId（波内同值，但保持逐行赋值的既有形态 ——
   * useDockerCmds 在 run 入口处求值，这一步是它切换主机的唯一钩子）。
   *
   * 波级结论点名到目标：成功的行名与失败的（行名 + 结论句）在这里就地收集 ——
   * 结论句的拼装是 utils/batch 的纯函数，本函数只负责把「哪行成了、哪行败了、
   * 为什么」如实递过去。失败同时并入跨全批的 failures（批末汇总仍只报前三条原因）。
   */
  async function runWave(
    wave: BatchWave,
    spec: BatchActionSpec,
    input: string,
    force: boolean,
    failures: BatchFailure[]
  ): Promise<number> {
    const host = wave.hostname !== '' ? wave.hostname : wave.hostId
    waveCurrent.value = { host, done: 0, total: wave.rows.length }
    const okNames: string[] = []
    const waveFails: BatchFailure[] = []
    let walked = 0
    for (const item of wave.rows) {
      // 分波按主机的纯函数只读 hostId/hostname，行对象原样是页面给的投影。
      const row = item as unknown as BatchRow
      rowHostId.value = row.hostId
      const gate = protectedGate({ protected: row.protected }, canExec.value)
      const forceAllowed = force && canExec.value
      const blockedMsg = row.blocked?.(spec.action) ?? null
      if (blockedMsg) {
        waveFails.push({ name: row.name, message: blockedMsg })
      } else if (gate.protected && !forceAllowed) {
        waveFails.push({ name: row.name, message: gate.conclusion })
      } else {
        const options: Record<string, unknown> = { ...(spec.options?.(row, input) ?? {}) }
        if (gate.protected && forceAllowed) options.force = true
        const res = await runCmd({ action: spec.action, target: row.name, options, key: 'batch' })
        if (res.ok) okNames.push(row.name)
        else waveFails.push({ name: row.name, message: runErrorMessage(res, '操作未完成') })
      }
      walked += 1
      waveCurrent.value = { host, done: walked, total: wave.rows.length }
    }
    failures.push(...waveFails)
    waveNotes.value = [
      ...waveNotes.value,
      { text: waveConclusion(wave, okNames, waveFails), failed: waveFails.length > 0 }
    ]
    return okNames.length
  }

  /** 结论句汇总：成功数 + 失败数 + 前三条失败原因（再多也读不完，余下只计数）。 */
  function summary(done: number, failures: BatchFailure[]): string {
    const head = `已执行 ${done} 项`
    if (failures.length === 0) return head
    const shown = failures.slice(0, 3).map((f) => `${f.name}（${f.message}）`)
    if (failures.length > shown.length) shown.push(`另有 ${failures.length - shown.length} 项`)
    return `${head}，${failures.length} 项失败：${shown.join('；')}`
  }
</script>

<style lang="scss" scoped>
  @use '../views/overview-tokens' as t;

  // 单根包装层：只为通过 single-root 守卫，不参与布局（display: contents 不产生盒）。
  .wkl-batch-root {
    display: contents;
  }

  // 勾选栏（含波次反馈）：选中或批量期间出现（随勾选在表下展开）。
  // 吸底（W1）：留在文档流尾部时，行数多则入口被挤到表卡可视区之外（实拍选中后
  // 入口被裁 / 完全不可见）。sticky 把整块钉在表卡滚动区底缘 —— 短视口（单滚动链，
  // app.scss 的 art-single-scroll-chain：卡体是 flex 列）与常规档（卡体 overflow:
  // hidden，内容溢出被裁）两档都常驻可视。背景取卡片底色（同值，无接缝），顶部
  // 1px 分隔把吸底块与滚过的表体分开；不设任何高度上限（表格高度政策不变）。
  .wkl-batch {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 12px 16px;

    position: sticky;
    bottom: 0;
    z-index: 3; // 高于表内容（本波未引入分页器，值先留位）
    background: var(--default-box-color);
    box-shadow: 0 -1px 0 var(--el-border-color-lighter);

    &__count {
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }

  // 波次反馈（6d）：勾选栏下方的行内账目，随吸底块同排（自占一整行 —— flex-basis
  // 100% 换行到入口行下方）。批量结束后保留到下一轮（批末 toast 转瞬即逝，账目要
  // 能回看）。
  .wkl-batch__waves {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1 0 100%;
  }

  .wkl-batch__wave {
    color: var(--el-text-color-secondary);
    font-size: 12px;
    line-height: 1.6;

    // 进行中的波：主题色 + 进行语气（与已完成波的账目口吻区分开）。
    &.is-current {
      // 主色文字对比度 AA（QA №9）：token 与数字见 @styles/core/aa-text.scss。
      color: var(--aa-primary-text);
    }

    // 有失败的波：如实用警示色（失败明细在批末汇总里，这里只挂「这波没全成」的旗）。
    &.is-failed {
      // 文字对比度 AA（收尾批）：原 el-color-warning（白底 1.85）改走 token。
      color: var(--aa-warning-text);
    }
  }

  // 确认弹窗：目标清单一屏看完（多了内部滚动），用户才知道这一下动的是哪些。
  // 跨主机表的同名行用 host 键区分（:key 也带上了），清单里给出归属主句。
  .wkl-batch-del {
    &__line {
      font-size: 13px;
      line-height: 1.6;
    }

    &__list {
      margin: 8px 0;
      padding-left: 18px;
      max-height: 180px;
      overflow: auto;
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }

    &__host {
      margin-left: 6px;
      font-size: 12px;
    }

    &__lock {
      display: inline-flex;
      gap: 4px;
      align-items: center;
      margin-left: 6px;
    }

    &__input {
      margin-top: 8px;
    }

    // 输入档（如导出 tar 的前缀）：标签 + 输入 + 提示，形制与单条确认弹窗的
    // 输入档同款（一条可见标签，不靠 placeholder 独撑）。
    &__label {
      margin: 8px 0 6px;
      font-size: 13px;
    }

    &__hint {
      margin-top: 6px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.6;
    }

    &__force {
      margin-top: 10px;
    }
  }
</style>