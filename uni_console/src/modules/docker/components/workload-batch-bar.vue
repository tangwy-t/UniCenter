<template>
  <!-- 单根包装（single-root 守卫扫全模块 .vue）：批量栏与删除确认弹窗是兄弟节点。 -->
  <div class="wkl-batch-root">
    <!-- 勾选栏（spec §11.1）：批量动作的入口。只有选中时才有意义，未选中时不占版面。 -->
    <div v-if="selected.length" class="wkl-batch">
      <span class="wkl-batch__count">已选 {{ selected.length }} 项</span>
      <ElButton v-if="canManage" size="small" :disabled="disabled" @click="run('container:start')">
        启动
      </ElButton>
      <ElButton v-if="canManage" size="small" :disabled="disabled" @click="run('container:stop')">
        停止
      </ElButton>
      <ElButton
        v-if="canManage"
        size="small"
        :disabled="disabled"
        @click="run('container:restart')"
      >
        重启
      </ElButton>
      <ElButton
        v-if="canDelete"
        size="small"
        type="danger"
        plain
        :disabled="disabled"
        @click="openRemove"
      >
        删除…
      </ElButton>
    </div>

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

    <!-- 批量删除：后端单条删除是标准档，但一次动多个目标，前端自加一道 DELETE 摩擦
         （计划 D2 / spec §11.6 —— 纯前端确认，不往协议加字段）。
         未复用 DockerActionConfirm：它的形态由动作注册表推导（container:remove 是标准档），
         表达不了「同一条动作在批量场景要更强确认」（缺口与建议见 2B 执行报告）。 -->
    <ElDialog v-model="remove.visible" title="删除确认" width="460px" :close-on-click-modal="false">
      <div class="wkl-batch-del">
        <div class="wkl-batch-del__line">将删除以下 {{ remove.rows.length }} 个容器：</div>
        <ul class="wkl-batch-del__list">
          <li v-for="row in remove.rows" :key="`${row.hostId}:${row.id}`">
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
        <div class="wkl-batch-del__line">此操作不可恢复。输入 DELETE 以确认：</div>
        <ElInput
          v-model="remove.input"
          class="wkl-batch-del__input"
          placeholder="DELETE"
          @keyup.enter="confirmRemove"
        />
      </div>
      <template #footer>
        <ElButton @click="remove.visible = false">取消</ElButton>
        <ElButton type="danger" :disabled="remove.input !== 'DELETE'" @click="confirmRemove">
          删除
        </ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  /**
   * 跨主机统一表的批量栏（从 containers.vue 下沉）：按钮 + 批量删除确认 + 逐行派发。
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
  import { computed, ref } from 'vue'
  import { ElButton, ElDialog, ElInput, ElMessage } from 'element-plus'
  import { PermDockerDelete, PermDockerExec, PermDockerManage } from '@/enums/permission'
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
  import { protectedGate } from '../utils/actions'

  defineOptions({ name: 'DockerWorkloadBatchBar' })

  const props = withDefaults(
    defineProps<{
      /** 当前勾选的行（页面持有，表格 selection-change 冒泡上来）。 */
      selected: DockerWorkloadItem[]
      /** 外部在途（页面单行指令通道的 busy）：批量与单行不并发提交。 */
      busy?: boolean
      /** 成功后的列表重拉（useDockerCmds 的 refresh）。 */
      refresh?: () => void | Promise<void>
    }>(),
    { selected: () => [], busy: false, refresh: undefined }
  )

  const emit = defineEmits<{
    /** 批量执行在途（起/止各发一次）：页面据此禁用行菜单，防并发提交同一目标。 */
    (e: 'busy-change', value: boolean): void
  }>()

  const { hasAuth } = useAuth()
  const canManage = computed(() => hasAuth(PermDockerManage))
  const canDelete = computed(() => hasAuth(PermDockerDelete))
  const canExec = computed(() => hasAuth(PermDockerExec))

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

  const remove = ref<{ visible: boolean; input: string; rows: DockerWorkloadItem[] }>({
    visible: false,
    input: '',
    rows: []
  })

  function openRemove() {
    remove.value = { visible: true, input: '', rows: [...props.selected] }
  }

  function confirmRemove() {
    if (remove.value.input !== 'DELETE') return
    const rows = remove.value.rows
    remove.value = { visible: false, input: '', rows: [] }
    void dispatch('container:remove', rows)
  }

  function run(action: 'container:start' | 'container:stop' | 'container:restart') {
    void dispatch(action, [...props.selected])
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
   * 受保护的目标批量不发送：批量没有「强制操作」开关可勾，发出去必被拒 ——
   * 直接按保护档的结论句计入失败，不浪费一条指令，也让原因可读（波级计数里
   * 它们算「未成功」）。
   */
  async function dispatch(action: string, rows: DockerWorkloadItem[]) {
    if (batchRunning.value || rows.length === 0) return
    // 复制一份：批量期间表格的勾选可能随重拉变化（行对象被换掉即清空），正在执行的
    // 这一批不能跟着变。分波对副本做（分组是执行口径，不是勾选口径）。
    const waves = groupBatchWaves([...rows])
    batchRunning.value = true
    emit('busy-change', true)
    waveNotes.value = []
    waveCurrent.value = null
    let done = 0
    const failures: BatchFailure[] = []
    try {
      for (const wave of waves) {
        done += await runWave(wave, action, failures)
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
   * 波级结论。返回该波成功行数（受保护未发送与失败行不算 —— 批末汇总另有明细）。
   * hostId 在每行派发前置为该行的 hostId（波内同值，但保持逐行赋值的既有形态 ——
   * useDockerCmds 在 run 入口处求值，这一步是它切换主机的唯一钩子）。
   *
   * 波级结论点名到目标：成功的行名与失败的（行名 + 结论句）在这里就地收集 ——
   * 结论句的拼装是 utils/batch 的纯函数（那里的注释有「为什么点名」），本函数
   * 只负责把「哪行成了、哪行败了、为什么」如实递过去。失败同时并入跨全批的
   * failures（批末汇总仍只报前三条原因，克制口径不变）。
   */
  async function runWave(
    wave: BatchWave,
    action: string,
    failures: BatchFailure[]
  ): Promise<number> {
    const host = wave.hostname !== '' ? wave.hostname : wave.hostId
    waveCurrent.value = { host, done: 0, total: wave.rows.length }
    const okNames: string[] = []
    const waveFails: BatchFailure[] = []
    let walked = 0
    for (const row of wave.rows) {
      rowHostId.value = row.hostId
      const gate = protectedGate({ protected: row.protected }, canExec.value)
      if (gate.protected) {
        waveFails.push({ name: row.name, message: gate.conclusion })
      } else {
        const res = await runCmd({ action, target: row.name, key: 'batch' })
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

  // 勾选栏：与底栏合计同一套留白，只有选中时出现。
  .wkl-batch {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
    padding: 12px 16px 0;

    &__count {
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }
  }

  // 波次反馈（6d）：勾选栏下方的行内账目。渲染不依赖勾选（批量中重拉会闪空勾选），
  // 自占一栏；批量结束后保留到下一轮（批末 toast 转瞬即逝，账目要能回看）。
  .wkl-batch__waves {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 8px 16px 0;
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

  // 批量删除弹窗：目标清单一屏看完（多了内部滚动），用户才知道这一下动的是哪些。
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
  }
</style>
