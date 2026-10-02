<template>
  <div class="log-viewer">
    <!-- 工具条：提示 + 行内查找 + 复制/下载 + 恢复跟随；Follow 与「暂停期间 N 行」是三期能力。 -->
    <div class="log-viewer__bar">
      <span v-if="followable" class="log-viewer__follow">
        <ElSwitch v-model="followingModel" size="small" />
        <span class="log-viewer__follow-label">跟随</span>
      </span>
      <span class="log-viewer__hint">{{ hint }}</span>
      <ElInput
        v-model="keyword"
        size="small"
        class="log-viewer__search"
        placeholder="在日志里查找"
        clearable
      />
      <ElButton size="small" @click="copyAll">复制</ElButton>
      <ElButton size="small" @click="download">下载</ElButton>
      <ElButton size="small" :disabled="!paused" @click="resume">继续滚动</ElButton>
      <!-- 暂停 = 停止渲染但继续缓冲：这里报的是「暂停之后又来了多少行」。 -->
      <span v-if="pausedCount > 0" class="log-viewer__buffered">
        暂停期间 {{ pausedCount }} 行
      </span>
    </div>
    <!--
      纯文本渲染：`{{ }}` 是文本插值，日志里的标签不会被解析。
      **不要**改成 v-html —— 日志内容是不可信文本，任何 HTML 拼接都是注入面。
    -->
    <pre ref="bodyRef" class="log-viewer__body" @scroll="onScroll">{{ rendered }}</pre>
  </div>
</template>

<script setup lang="ts">
  import { computed, nextTick, ref, watch } from 'vue'
  import { ElButton, ElInput, ElMessage, ElSwitch } from 'element-plus'
  import { logHint, visibleLines } from '../utils/log'

  const props = withDefaults(
    defineProps<{
      lines: string
      truncated?: boolean
      /** 三期：是否渲染 Follow 开关（权限与期次的双重把关在调用方，不渲染 ≠ 禁用）。 */
      followable?: boolean
      /** 是否正在跟随日志流。 */
      following?: boolean
      /** 跟随缓冲的累计行数（暂停计数与「共 N 行」用；一次性模式下不传）。 */
      totalLines?: number
    }>(),
    { truncated: false, followable: false, following: false, totalLines: 0 }
  )

  const emit = defineEmits<{ 'update:following': [value: boolean] }>()

  /** 开关是受控的：起停流（建会话/断开）由调用方按 following 的翻转执行。 */
  const followingModel = computed({
    get: () => props.following,
    set: (value: boolean) => emit('update:following', value)
  })

  const keyword = ref('')
  /** 用户上滚 = 想看历史 → 暂停自动滚动（由滚动事件自动置位，底部「继续滚动」按钮复位）。 */
  const paused = ref(false)
  const bodyRef = ref<HTMLElement | null>(null)

  /**
   * 冻结的渲染源（暂停那一刻的文本）。
   *
   * 跟随模式下暂停 = **停止渲染但继续缓冲**：新行继续进缓冲（计数），画面停在原处，
   * 点「继续滚动」一次性把缓冲里攒下的全部追加出来。一次性取日志没有「新行到来」，
   * 暂停只影响自动滚动，故不冻结。
   */
  const frozen = ref<string | null>(null)
  /** 冻结那一刻的累计行数：暂停期间 N 行 = 当前累计 − 它。 */
  const frozenTotal = ref(0)

  const source = computed(() => frozen.value ?? props.lines)
  /** 缓冲上限（5000 行）与关键字过滤都在 utils/log 的纯函数里（那里有测试）。 */
  const visible = computed(() => visibleLines(source.value, keyword.value))
  const rendered = computed(() => visible.value.text)

  const pausedCount = computed(() =>
    frozen.value !== null && props.following ? Math.max(0, props.totalLines - frozenTotal.value) : 0
  )

  /**
   * 提示：后端因行数上限截断（载荷的 truncated）与本地缓冲截断，说的是同一件事 ——
   * 「你看到的不是全部」。两者都归到这一句话上：用户关心的只是「我看到的全不全」，
   * 而不全的原因（服务端只回了这么多 / 页面只渲染这么多）对判断没有区别。
   * 数字用缓冲上限：它才是「最多能看到多少」的那个数；跟随流的累计行数由流侧给。
   */
  const hint = computed(() =>
    logHint(
      source.value,
      props.truncated || visible.value.truncated,
      // 冻结时说冻结那一刻的实话（屏上就是那一屏），攒下的行数由「暂停期间 N 行」单报。
      props.following && frozen.value === null ? props.totalLines : undefined
    )
  )

  /**
   * 上滚即暂停。
   *
   * 阈值 8px：亚像素滚动（缩放/触控板）到底时仍会留下 1~2px 的余量，不设阈值会把
   * 「已经到底了」误判成「用户在上滚」，于是新日志再也不跟随。
   */
  function onScroll() {
    const el = bodyRef.value
    if (!el) return
    const atBottom = el.scrollTop + el.clientHeight >= el.scrollHeight - 8
    if (atBottom) {
      paused.value = false
      // 自己滚回底部 = 恢复实时（与「继续滚动」同一语义：把缓冲里攒下的都显示出来）。
      frozen.value = null
      return
    }
    if (!paused.value) {
      paused.value = true
      if (props.following) freeze()
    }
  }

  function freeze() {
    frozen.value = props.lines
    frozenTotal.value = props.totalLines
  }

  function resume() {
    paused.value = false
    frozen.value = null
    void scrollToBottom()
  }

  async function scrollToBottom() {
    await nextTick()
    const el = bodyRef.value
    if (el) el.scrollTop = el.scrollHeight
  }

  // 跟随停止（用户关闭/切走/断流）：解冻，让缓冲里攒下的内容一次性落屏（不清 paused：
  // 用户可能正停在历史位置阅读，解冻后的内容接在他读的位置**之后**，位置不会被顶走）。
  // 跟随开始时若已经处于暂停（用户先上滚再看开关），同样立刻冻结 —— 否则「暂停期间 N 行」
  // 会从 0 开始算，用户上滚停在的位置也会被新内容顶走。
  watch(
    () => props.following,
    (on) => {
      if (!on) {
        frozen.value = null
        return
      }
      if (paused.value) freeze()
    }
  )

  // 新日志到达时贴底（一次性模式是点「拉取」后才到来；跟随模式是持续到来）——
  // 除非用户已暂停：那时把他正在读的位置顶走是不可接受的。
  watch(
    () => props.lines,
    () => {
      if (!paused.value) void scrollToBottom()
    }
  )

  async function copyAll() {
    try {
      await navigator.clipboard.writeText(rendered.value)
      ElMessage.success('已复制')
    } catch {
      // 非安全上下文（http 访问）里 clipboard 不可用：给一句可执行的替代做法，而不是弹「失败」。
      ElMessage.warning('复制失败，请手动选择文本')
    }
  }

  function download() {
    const blob = new Blob([rendered.value], { type: 'text/plain;charset=utf-8' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'container.log'
    a.click()
    URL.revokeObjectURL(a.href)
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../views/overview-tokens' as t;

  // 「复制 / 下载 / 继续滚动」等默认档按钮的主色文字对比度 AA：病灶与处方见
  // overview-tokens 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  .log-viewer {
    display: flex;
    flex-direction: column;
    gap: 8px;

    &__bar {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 8px;
    }

    &__follow {
      display: inline-flex;
      gap: 6px;
      align-items: center;
    }

    &__follow-label {
      font-size: 13px;
    }

    &__hint {
      // 次要文字色：这行是「你看到的是哪一段」的注脚，不与日志正文争夺注意力。
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }

    &__search {
      width: 220px;
    }

    // 暂停计数是「有东西在等着你」的提示：琥珀色即结论（与模块里的陈旧/保护同一套语言）。
    &__buffered {
      color: var(--el-color-warning);
      font-size: 13px;
    }

    &__body {
      box-sizing: border-box;
      min-height: 240px;
      max-height: 560px;
      margin: 0;
      padding: 12px;
      overflow: auto;
      background: var(--el-fill-color-light);
      border-radius: 6px;
      // 长行折行而不是横向滚动：一行超长的日志（堆栈、JSON）会让人只能左右拖着看。
      white-space: pre-wrap;
      word-break: break-word;
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      font-size: 12px;
      line-height: 1.7;
    }
  }

  /* ── 响应式 ─────────────────────────────────────── */

  // 窄屏（<1024，覆盖平板竖屏与手机横屏）：正文上限跟视口高度挂钩 —— 固定 560px
  // 在低矮的横屏窗口里会把正文顶到屏幕之外，日志区和工具栏无法同屏。
  @include respond-below('desktop') {
    .log-viewer__body {
      max-height: min(560px, 60vh);
    }
  }

  // 手机横屏（<768）：查找框改成可伸缩（不再固定 220px），与其它工具项同排换行；
  // 「共 N 行」的注脚挪到工具行之后独占一行，不跟按钮抢宽度。
  @include respond-below('tablet') {
    .log-viewer__bar {
      gap: 6px;
    }

    .log-viewer__hint {
      flex: 1 1 100%;
      order: 10;
    }

    .log-viewer__search {
      width: auto;
      min-width: 0;
      flex: 1 1 180px;
    }

    .log-viewer__body {
      min-height: 200px;
    }
  }
</style>
