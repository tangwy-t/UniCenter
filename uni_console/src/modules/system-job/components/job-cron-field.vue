<!-- Cron 执行周期输入组件：快捷预设 / 可视化生成器 / 原始表达式三模式。
     单一数据模型（6 段秒级表达式），实时输出人类可读描述 + 未来执行预览。
     本地解析失败时仅提示，最终权威校验在服务端（robfig/cron v3）。 -->
<template>
  <div class="job-cron-field">
    <ElRadioGroup v-model="mode" size="small" class="cron-mode-switch">
      <ElRadioButton value="quick">快捷</ElRadioButton>
      <ElRadioButton value="visual">可视化</ElRadioButton>
      <ElRadioButton value="expr">表达式</ElRadioButton>
    </ElRadioGroup>

    <div class="cron-panel">
      <!-- 快捷预设 -->
      <template v-if="mode === 'quick'">
        <div class="preset-grid">
          <button
            v-for="p in CRON_PRESETS"
            :key="p.value"
            type="button"
            :class="['preset-chip', { active: displayExpr === p.value }]"
            @click="apply(p.value)"
          >
            {{ p.label }}
          </button>
        </div>
      </template>

      <!-- 可视化生成器 -->
      <template v-else-if="mode === 'visual'">
        <div class="pattern-row">
          <ElSelect v-model="sub.pattern" class="pattern-select" aria-label="调度模式">
            <ElOption label="每 N 时间单位" value="interval" />
            <ElOption label="每日指定时刻" value="daily" />
            <ElOption label="每周指定几日" value="weekly" />
            <ElOption label="每月指定日期" value="monthly" />
          </ElSelect>
        </div>
        <div class="visual-row">
          <template v-if="sub.pattern === 'interval'">
            <span class="inline-label">每</span>
            <ElInputNumber
              v-model="sub.n"
              :min="1"
              :max="intervalMax"
              controls-position="right"
              class="num-input"
              aria-label="间隔数值"
            />
            <ElSelect v-model="sub.unit" class="unit-select" aria-label="时间单位">
              <ElOption label="秒" value="sec" />
              <ElOption label="分钟" value="min" />
              <ElOption label="小时" value="hour" />
            </ElSelect>
            <span class="inline-label">执行一次</span>
          </template>

          <template v-else>
            <template v-if="sub.pattern === 'weekly'">
              <ElSelect
                v-model="sub.days"
                multiple
                collapse-tags
                collapse-tags-tooltip
                :max-collapse-tags="1"
                class="days-select"
                aria-label="星期"
              >
                <ElOption
                  v-for="d in WEEK_OPTIONS"
                  :key="d.value"
                  :label="d.label"
                  :value="d.value"
                />
              </ElSelect>
              <span class="inline-label">的</span>
            </template>
            <template v-else-if="sub.pattern === 'monthly'">
              <span class="inline-label">每月</span>
              <ElInputNumber
                v-model="sub.dom"
                :min="1"
                :max="31"
                controls-position="right"
                class="num-input"
                aria-label="日期"
              />
              <span class="inline-label">号</span>
            </template>
            <ElInputNumber
              v-model="sub.hour"
              :min="0"
              :max="23"
              controls-position="right"
              class="num-input"
              aria-label="时"
            />
            <span class="inline-label">:</span>
            <ElInputNumber
              v-model="sub.minute"
              :min="0"
              :max="59"
              controls-position="right"
              class="num-input"
              aria-label="分"
            />
            <span class="inline-label">{{
              sub.pattern === 'daily' ? '执行' : '执行（秒=0）'
            }}</span>
          </template>
        </div>
        <div
          v-if="sub.pattern === 'interval' && sub.unit === 'sec' && sub.n > 0 && sub.n < 10"
          class="visual-tip"
        >
          <ArtSvgIcon icon="ri:information-line" />
          秒级高频调度会持续占用执行线程与锁，请确认必要性
        </div>
      </template>

      <!-- 原始表达式 -->
      <template v-else>
        <ElInput v-model="exprText" class="expr-input font-mono" placeholder="0 */5 * * * *">
          <template #prepend>表达式</template>
        </ElInput>
        <div class="expr-hint">
          支持 5 段（分 时 日 月 周）/ 6 段（秒 分 时 日 月 周）数字表达式，以及
          <span class="font-mono">@every 5m</span>、<span class="font-mono">@hourly</span> 等描述符
        </div>
      </template>

      <!-- 即时反馈：人类可读 + 预览 + 校验 -->
      <div class="cron-feedback" :class="{ invalid: !evaluation.valid }">
        <div v-if="evaluation.valid && evaluation.description" class="feedback-line description">
          <ArtSvgIcon icon="ri:translate-2" />
          <span>{{ evaluation.description }}</span>
        </div>
        <div v-if="evaluation.valid && evaluation.nextRuns.length" class="feedback-line preview">
          <ArtSvgIcon icon="ri:time-line" />
          <span class="label">最近 {{ evaluation.nextRuns.length }} 次执行：</span>
          <span v-for="t in evaluation.nextRuns" :key="t.getTime()" class="chip">
            {{ formatShortTime(t) }}
          </span>
          <span class="hint">（按浏览器时区估算）</span>
        </div>
        <div
          v-if="evaluation.valid && !evaluation.description && !evaluation.nextRuns.length"
          class="feedback-line hint-line"
        >
          <ArtSvgIcon icon="ri:information-line" />
          该表达式将交由服务端解析校验
        </div>
        <div v-if="!evaluation.valid" class="feedback-line error">
          <ArtSvgIcon icon="ri:error-warning-line" />
          <span>{{ evaluation.message }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { CRON_PRESETS, evaluateCron, formatShortTime } from '../utils/cron'

  defineOptions({ name: 'JobCronField' })

  const props = defineProps<{ modelValue: string }>()
  const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

  type CronMode = 'quick' | 'visual' | 'expr'

  interface VisualSub {
    pattern: 'interval' | 'daily' | 'weekly' | 'monthly'
    unit: 'sec' | 'min' | 'hour'
    n: number
    days: number[]
    dom: number
    hour: number
    minute: number
  }

  const WEEK_OPTIONS = [
    { label: '周日', value: 0 },
    { label: '周一', value: 1 },
    { label: '周二', value: 2 },
    { label: '周三', value: 3 },
    { label: '周四', value: 4 },
    { label: '周五', value: 5 },
    { label: '周六', value: 6 }
  ]

  const mode = ref<CronMode>('quick')
  const exprText = ref('')
  const sub = reactive<VisualSub>({
    pattern: 'interval',
    unit: 'min',
    n: 5,
    days: [1],
    dom: 1,
    hour: 3,
    minute: 0
  })

  /** 当前展示用的表达式（跟随父级 modelValue） */
  const displayExpr = computed(() => props.modelValue ?? '')
  /** 本地评估结果（描述/预览/校验提示） */
  const evaluation = computed(() => evaluateCron(displayExpr.value))

  const intervalMax = computed(() => (sub.unit === 'sec' || sub.unit === 'min' ? 59 : 23))

  /** 从可视化子模型生成 6 段表达式 */
  function buildFromSub(): string | undefined {
    const n = Math.max(1, Math.floor(sub.n) || 1)
    const hour = Math.max(0, Math.min(23, Math.floor(sub.hour)))
    const minute = Math.max(0, Math.min(59, Math.floor(sub.minute)))
    switch (sub.pattern) {
      case 'interval':
        if (sub.unit === 'sec') return `*/${n} * * * * *`
        if (sub.unit === 'min') return `0 */${n} * * * *`
        return `0 0 */${n} * * *`
      case 'daily':
        return `0 ${minute} ${hour} * * *`
      case 'weekly': {
        const days = [...new Set(sub.days)].sort((a, b) => a - b)
        return days.length ? `0 ${minute} ${hour} * * ${days.join(',')}` : undefined
      }
      case 'monthly':
        return `0 ${minute} ${hour} ${Math.max(1, Math.floor(sub.dom) || 1)} * *`
      default:
        return undefined
    }
  }

  /** 仅按可视化正则还原子模型（跳过快捷预设短路，供切换到可视化时对齐） */
  function deriveVisualSub(t: string): Partial<VisualSub> | undefined {
    let m: RegExpMatchArray | null
    if ((m = t.match(/^\*\/(\d+) \* \* \* \* \*$/))) {
      return { pattern: 'interval', unit: 'sec', n: Number(m[1]) }
    }
    if ((m = t.match(/^0 \*\/(\d+) \* \* \* \*$/))) {
      return { pattern: 'interval', unit: 'min', n: Number(m[1]) }
    }
    if ((m = t.match(/^0 0 \*\/(\d+) \* \* \*$/))) {
      return { pattern: 'interval', unit: 'hour', n: Number(m[1]) }
    }
    if ((m = t.match(/^0 (\d{1,2}) (\d{1,2}) \* \* \*$/))) {
      return { pattern: 'daily', minute: Number(m[1]), hour: Number(m[2]) }
    }
    const wk = t.match(/^0 (\d{1,2}) (\d{1,2}) \* \* ([0-7](?:,[0-7])*)$/)
    if (wk) {
      return {
        pattern: 'weekly',
        minute: Number(wk[1]),
        hour: Number(wk[2]),
        days: wk[3].split(',').map(Number)
      }
    }
    const mo = t.match(/^0 (\d{1,2}) (\d{1,2}) (\d{1,2}) \* \*$/)
    if (mo) {
      return {
        pattern: 'monthly',
        minute: Number(mo[1]),
        hour: Number(mo[2]),
        dom: Number(mo[3])
      }
    }
    return undefined
  }

  /** 解析已有表达式，尽量还原到可视化子模型（还原不了时落入表达式模式） */
  function derive(expr: string): { mode: CronMode; sub?: Partial<VisualSub> } {
    const t = expr.trim()
    if (!t) return { mode: 'quick' }
    if (CRON_PRESETS.some((p) => p.value === t)) return { mode: 'quick' }
    const sub = deriveVisualSub(t)
    return sub ? { mode: 'visual', sub } : { mode: 'expr' }
  }

  /** 组件内部主动上抛的表达式：父级回传同值时跳过模式回推，
   *  避免同名快捷预设（如「每 5 分钟」）把可视化模式拉回快捷页 */
  let selfEmitted: string | null = null

  /** 父级值变化 → 还原模式与可视化子模型（编辑回填/预设应用） */
  watch(
    () => props.modelValue,
    (val) => {
      const t = val ?? ''
      exprText.value = t
      // 内部上抛的回传：保持当前编辑模式，不回推
      if (selfEmitted !== null && selfEmitted === t) {
        selfEmitted = null
        return
      }
      const d = derive(t)
      mode.value = d.mode
      if (d.sub) Object.assign(sub, d.sub)
    },
    { immediate: true }
  )

  /** 切到可视化模式：有表达式时把子模型对齐到该表达式（含快捷预设）；
   *  为空则立即按当前子模型生成默认表达式。sync flush 保证在表单项
   *  change 校验触发前生效，避免“明明有值却提示请填写执行周期” */
  watch(
    mode,
    (m) => {
      if (m !== 'visual') return
      const current = (props.modelValue ?? '').trim()
      if (current) {
        const visualSub = deriveVisualSub(current)
        if (visualSub) Object.assign(sub, visualSub)
        return
      }
      const expr = buildFromSub()
      if (expr) {
        selfEmitted = expr
        emit('update:modelValue', expr)
      }
    },
    { flush: 'sync' }
  )

  /** 可视化子模型变化 → 生成表达式上抛 */
  watch(
    sub,
    () => {
      if (mode.value !== 'visual') return
      const expr = buildFromSub()
      if (expr && expr !== props.modelValue) {
        selfEmitted = expr
        emit('update:modelValue', expr)
      }
    },
    { deep: true }
  )

  /** 表达式模式输入 → 上抛 */
  watch(exprText, (val) => {
    if (mode.value === 'expr' && val !== props.modelValue) {
      selfEmitted = val
      emit('update:modelValue', val)
    }
  })

  /** 快捷预设应用 */
  function apply(expr: string) {
    emit('update:modelValue', expr)
  }

  /** 供表单规则调用：返回本地校验结果（服务端仍为最终权威） */
  function validate() {
    return { valid: evaluation.value.valid, message: evaluation.value.message }
  }

  defineExpose({ validate })
</script>

<style scoped>
  .job-cron-field {
    width: 100%;
  }

  .cron-mode-switch {
    margin-bottom: 10px;
  }

  .cron-panel {
    padding: 12px;
    border: 1px dashed var(--default-border-dashed, #dbdfe9);
    border-radius: 8px;
    background: var(--default-bg-color, #fafbfc);
  }

  .dark .cron-panel {
    background: var(--art-gray-200, #17171c);
  }

  /* 快捷预设网格 */
  .preset-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
    gap: 8px;
  }

  .preset-chip {
    padding: 7px 10px;
    border: 1px solid var(--default-border, #e2e8ee);
    border-radius: 6px;
    background: var(--default-box-color, #fff);
    color: var(--color-g-700, #4d5875);
    font-size: 13px;
    line-height: 1.4;
    cursor: pointer;
    transition:
      border-color 150ms ease,
      color 150ms ease,
      background-color 150ms ease;
  }

  .preset-chip:hover {
    border-color: var(--theme-color);
    color: var(--theme-color);
  }

  .preset-chip.active {
    border-color: var(--theme-color);
    background: color-mix(in srgb, var(--theme-color) 12%, transparent);
    color: var(--theme-color);
    font-weight: 600;
  }

  /* 可视化行 */
  .visual-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }

  .inline-label {
    font-size: 13px;
    color: var(--color-g-600, #7987a1);
    white-space: nowrap;
  }

  .pattern-row {
    margin-bottom: 8px;
  }

  .pattern-select {
    width: 100%;
  }

  .unit-select {
    width: 90px;
  }

  .days-select {
    width: 132px;
  }

  .num-input {
    width: 88px;
  }

  .visual-tip {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: 8px;
    font-size: 12px;
    color: var(--color-warning, #d97706);
  }

  /* 表达式模式 */
  .expr-input {
    width: 100%;
  }

  .expr-hint {
    margin-top: 6px;
    font-size: 12px;
    color: var(--color-g-500, #949eb7);
  }

  /* 反馈区 */
  .cron-feedback {
    margin-top: 10px;
  }

  .feedback-line {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    margin-bottom: 4px;
  }

  .feedback-line.description {
    color: var(--color-g-700, #4d5875);
  }

  .feedback-line.preview .label {
    color: var(--color-g-600, #7987a1);
  }

  .feedback-line .chip {
    padding: 1px 8px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--theme-color) 10%, transparent);
    color: var(--theme-color);
    font-family: ui-monospace, 'Fira Code', 'SF Mono', Consolas, monospace;
    font-size: 12px;
  }

  .feedback-line .hint {
    font-size: 12px;
    color: var(--color-g-500, #949eb7);
  }

  .feedback-line.hint-line {
    color: var(--color-g-500, #949eb7);
  }

  .feedback-line.error {
    color: var(--color-danger, #dc2626);
  }

  .cron-feedback.invalid {
    .feedback-line.error {
      font-weight: 500;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .preset-chip {
      transition: none;
    }
  }
</style>
