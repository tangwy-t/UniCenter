<template>
  <div class="log-viewer">
    <!-- 工具条：提示 + 行内查找 + 复制/下载 + 恢复跟随。跟随开关是三期能力，一期不渲染。 -->
    <div class="log-viewer__bar">
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
  import { ElButton, ElInput, ElMessage } from 'element-plus'
  import { logHint, visibleLines } from '../utils/log'

  const props = withDefaults(defineProps<{ lines: string; truncated?: boolean }>(), {
    truncated: false
  })

  const keyword = ref('')
  /** 用户上滚 = 想看历史 → 暂停自动滚动（由滚动事件自动置位，底部「继续滚动」按钮复位）。 */
  const paused = ref(false)
  const bodyRef = ref<HTMLElement | null>(null)

  /** 缓冲上限（5000 行）与关键字过滤都在 utils/log 的纯函数里（那里有测试）。 */
  const visible = computed(() => visibleLines(props.lines, keyword.value))
  const rendered = computed(() => visible.value.text)

  /**
   * 提示：后端因行数上限截断（载荷的 truncated）与本地缓冲截断，说的是同一件事 ——
   * 「你看到的不是全部」。两者都归到这一句话上：用户关心的只是「我看到的全不全」，
   * 而不全的原因（服务端只回了这么多 / 页面只渲染这么多）对判断没有区别。
   * 数字用缓冲上限：它才是「最多能看到多少」的那个数。
   */
  const hint = computed(() => logHint(props.lines, props.truncated || visible.value.truncated))

  /**
   * 上滚即暂停。
   *
   * 阈值 8px：亚像素滚动（缩放/触控板）到底时仍会留下 1~2px 的余量，不设阈值会把
   * 「已经到底了」误判成「用户在上滚」，于是新日志再也不跟随。
   */
  function onScroll() {
    const el = bodyRef.value
    if (!el) return
    paused.value = el.scrollTop + el.clientHeight < el.scrollHeight - 8
  }

  function resume() {
    paused.value = false
    void scrollToBottom()
  }

  async function scrollToBottom() {
    await nextTick()
    const el = bodyRef.value
    if (el) el.scrollTop = el.scrollHeight
  }

  // 新日志到达时贴底（一期是「按需拉取」，新日志只在点「拉取」后到来）——
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
  .log-viewer {
    display: flex;
    flex-direction: column;
    gap: 8px;

    &__bar {
      display: flex;
      align-items: center;
      gap: 8px;
    }

    &__hint {
      // 次要文字色：这行是「你看到的是哪一段」的注脚，不与日志正文争夺注意力。
      color: var(--el-text-color-secondary);
      font-size: 13px;
    }

    &__search {
      width: 220px;
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
</style>
