<template>
  <!-- 单根（single-root 守卫扫全模块的 .vue）：参数块本体是唯一根。 -->
  <div class="to">
    <div class="to__head">
      <span class="to__title">参数</span>
      <button type="button" class="to__copy" title="复制" @click="copy">
        <ArtSvgIcon icon="ri:file-copy-line" />
      </button>
      <!-- 截断是硬事实（尾标 ...(truncated) 是服务端给的原文信号）：如实标注一行，
           不假装完整。 -->
      <span v-if="truncated" class="to__trunc">已截断</span>
    </div>

    <!-- exec：argv 拼成一行可读命令行（展示用 shell 转义，不拼可执行串）。 -->
    <p v-if="commandLine !== null" class="to__cmd">{{ commandLine }}</p>

    <!-- 键值清单：中文标签优先，查不到就原样键名 + 等宽值。 -->
    <dl v-if="fields.length" class="to__kv">
      <template v-for="f in fields" :key="f.key">
        <dt class="to__key" :class="{ 'is-raw': !f.labeled }" :title="f.key">{{ f.label }}</dt>
        <dd
          class="to__val"
          :class="{ 'is-block': f.block }"
          :title="f.block ? undefined : f.text"
          >{{ f.text }}</dd
        >
      </template>
    </dl>

    <!-- 原文兜底：截断（半截 JSON 非合法 JSON）或非法 JSON 时如实给原文。 -->
    <pre v-if="rawFallback" class="to__raw">{{ rawText }}</pre>
  </div>
</template>

<script setup lang="ts">
  /**
   * 任务行展开区里的**参数块**（任务中心「能看见具体执行的命令么」的落点）。
   *
   * 数据是受理时下发的整份 DockerCmdOptions 的 JSON 原文（task 条目的 options 字段）。
   * 三条必须照此实现的语义：
   *   - **字段缺席 = 没有看这笔任务参数的资格**（不是空串、不是掩码）：组件不由页面
   *     在缺席时挂载，本组件也只在拿到非空串时才有意义（调用方门控）；
   *   - **超 16 KiB 已截断**且尾部带 `...(truncated)` 标（截断后**不是合法 JSON**）：
   *     解析必须容错 —— 见到尾标即放弃解析、走原文兜底，并如实标注「已截断」；
   *   - exec 的 `command` 是 argv（协议 `DockerCmdOptions.Command`，缺省 `/bin/sh`）。
   *
   * 渲染口径：
   *   - **exec**：把 `command` 数组拼成一行命令行 —— 用户真正想看的东西，要一眼可读。
   *     参数按 POSIX shell 规则做**展示用转义**（含空格/引号的参数包单引号），只用于
   *     显示与复制，**绝不**拼成可执行串去做 eval；
   *   - **其它动作**：键值清单（中文标签优先，查不到标签原样键名 + 等宽值）；嵌套对象 /
   *     数组 / 多行长值（如 compose 的 YAML 正文）用等宽块展示，可折行可滚；
   *   - **截断/非法**：原文等宽块 + 「已截断」标注。
   *
   * 复制：exec 复制命令行（可直接粘进终端），其余复制**原文**（保真路径）。
   */
  import { computed } from 'vue'
  import { ElMessage } from 'element-plus'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { copyText as copyToClipboard } from '@/utils/ui/clipboard'

  defineOptions({ name: 'DockerTaskOptions' })

  const props = defineProps<{
    /** 受理时下发的动作参数原文（JSON 文本，可能带截断尾标）。 */
    options: string
    /** 动作码：exec 走命令行形态，其余走键值清单。 */
    action: string
  }>()

  /** 服务端截断尾标（middleware.TruncateString 的固定形态，逐字对齐）。 */
  const TRUNC_MARK = '...(truncated)'

  const truncated = computed(() => props.options.endsWith(TRUNC_MARK))

  /** 去掉尾标后的文本：解析与展示都用它（尾标本身只作「已截断」的判据）。 */
  const rawText = computed(() =>
    truncated.value ? props.options.slice(0, -TRUNC_MARK.length) : props.options
  )

  /**
   * 解析原文。**截断即放弃解析**（半截 JSON 不是合法 JSON，硬解只会得到错值或抛错）；
   * 非对象（数组/标量）也不进键值面 —— 都走原文兜底。
   */
  const parsed = computed<Record<string, unknown> | null>(() => {
    if (truncated.value) return null
    try {
      const v: unknown = JSON.parse(rawText.value)
      if (v !== null && typeof v === 'object' && !Array.isArray(v)) {
        return v as Record<string, unknown>
      }
    } catch {
      // 非法 JSON：原文兜底（不该发生，但容错是硬要求）。
    }
    return null
  })

  /** POSIX shell 的「安全字符」：命中的参数原样显示，其余包单引号（展示用，不执行）。 */
  const SHELL_SAFE = /^[A-Za-z0-9_@%+=:,./-]+$/

  /** 单个 argv 的**展示用**转义：含空格/引号等的参数包单引号，内嵌单引号按 `'\''` 转义。 */
  function shellArg(arg: string): string {
    if (arg === '') return "''"
    if (SHELL_SAFE.test(arg)) return arg
    return `'${arg.replace(/'/g, "'\\''")}'`
  }

  /**
   * exec 的命令行（只对 `container:exec` 生成）：
   *   - 有 command → 逐项转义后空格连接（`ls -la`）；
   *   - 缺省/空数组 → 协议缺省 `["/bin/sh"]`（空数组与缺席同义），照实给出**实际会跑的 argv**；
   *   - 原文解析不出（截断/非法）→ null，改走原文兜底。
   */
  const commandLine = computed<string | null>(() => {
    if (props.action !== 'container:exec') return null
    const p = parsed.value
    if (p === null) return null
    const cmd = p.command
    const argv =
      Array.isArray(cmd) && cmd.length > 0 ? cmd.map((a) => String(a)) : ['/bin/sh']
    return argv.map(shellArg).join(' ')
  })

  /** 键值字段的中文标签（DockerCmdOptions 字段名 → 人话；查不到的原样键名）。 */
  const FIELD_LABELS: Record<string, string> = {
    target: '目标',
    tail: '尾部行数',
    since: '起始时刻',
    n: '实例数',
    force: '强制',
    filename: '文件名',
    overwrite: '覆盖',
    all: '清理全部',
    remove_orphans: '回收孤儿',
    volumes: '连带删除卷',
    content: '文件内容',
    base_hash: '基线哈希',
    src: '源',
    dst: '目的地',
    patch: '增量变更',
    follow: '跟随',
    command: '命令',
    backup: '备份令牌',
    image: '镜像',
    name: '容器名',
    ports: '端口',
    env: '环境变量',
    mounts: '挂载',
    restart_policy: '重启策略',
    cpu_limit: 'CPU 上限',
    mem_limit_mb: '内存上限',
    network: '网络',
    start: '创建后启动',
    registry: '仓库',
    context: '构建上下文',
    dockerfile: 'Dockerfile',
    tag: '标签',
    args: '构建参数'
  }

  /** 一个键值字段的展示形态：文本 + 是否走等宽块（长值/嵌套/多行）。 */
  interface FieldView {
    key: string
    label: string
    /** 标签是否来自注册表（否则是原样键名，调用处加等宽 class）。 */
    labeled: boolean
    text: string
    /** 等宽块（可折行可滚）：嵌套对象/数组、多行或过长的单行值。 */
    block: boolean
  }

  /** 单值 → 展示形态（长值/嵌套走等宽块，短标量走行内等宽）。 */
  function viewValue(v: unknown): { text: string; block: boolean } {
    if (v === null || v === undefined) return { text: '—', block: false }
    if (typeof v === 'boolean') return { text: v ? '是' : '否', block: false }
    if (typeof v === 'number') return { text: String(v), block: false }
    if (typeof v === 'string') {
      if (v === '') return { text: '—', block: false }
      // 多行（如 compose 的 YAML 正文）或过长单行：等宽块。
      return { text: v, block: v.includes('\n') || v.length > 40 }
    }
    if (Array.isArray(v)) {
      // 数组：标量项每项一行（端口/环境变量/挂载读起来是一列）；否则 JSON 缩进。
      if (v.every((x) => typeof x === 'string' || typeof x === 'number')) {
        return { text: v.map((x) => String(x)).join('\n'), block: true }
      }
      return { text: JSON.stringify(v, null, 2), block: true }
    }
    // 嵌套对象（如 patch / args）：JSON 缩进块。
    return { text: JSON.stringify(v, null, 2), block: true }
  }

  /**
   * 键值字段清单：
   *   - exec 的 `command` 已被命令行形态吃掉；`target` 是列表的目标列已经说过的同一个
   *     事实（重复一次只是噪音）—— 两者从键值面里略去，其余字段照实列出（未来若 exec
   *     长出新字段不会被这里吞掉）；
   *   - 其余动作：全部字段照实列出。
   */
  const ELIDED_FOR_EXEC = new Set(['command', 'target'])

  const fields = computed<FieldView[]>(() => {
    const p = parsed.value
    if (p === null) return []
    const out: FieldView[] = []
    for (const [key, value] of Object.entries(p)) {
      if (props.action === 'container:exec' && ELIDED_FOR_EXEC.has(key)) continue
      const label = FIELD_LABELS[key]
      const { text, block } = viewValue(value)
      out.push({ key, label: label ?? key, labeled: label !== undefined, text, block })
    }
    return out
  })

  /** 原文兜底：解析不出（截断/非法）且确有原文时给原文块。 */
  const rawFallback = computed(() => parsed.value === null && rawText.value !== '')

  /** 复制：exec 复制命令行（可粘贴进终端），其余复制原文（保真路径）。 */
  const copyText = computed(() => commandLine.value ?? props.options)

  async function copy(): Promise<void> {
    // 共享助手：先试 Clipboard API，失败自动回退 execCommand（http 非安全上下文可用）。
    const ok = await copyToClipboard(copyText.value)
    if (ok) {
      ElMessage.success('已复制')
    } else {
      // 连回退都失败（极罕见）：给一句可执行的替代做法，而不是干巴巴的「失败」。
      ElMessage.warning('复制失败，请手动选择文本')
    }
  }
</script>

<style lang="scss" scoped>
  // 参数块：接在进度流 / 结论句之后的**追加块**（块间用上边距分开，不新增边框语言）。
  .to {
    margin-top: 6px;
  }

  .to__head {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 4px;
  }

  // 块的硬标签（与表头同族：名词，不是解释句）。
  .to__title {
    color: var(--el-text-color-secondary);
    font-size: 12px;
    font-weight: 600;
  }

  // 复制钮：与展开开关同款的纯图标热区（无边框、悬停变主色）。
  .to__copy {
    display: inline-flex;
    width: 22px;
    height: 22px;
    align-items: center;
    justify-content: center;
    padding: 0;
    border: none;
    background: none;
    color: var(--el-text-color-secondary);
    cursor: pointer;

    &:hover {
      color: var(--el-color-primary);
    }
  }

  // 截断标注：硬事实一行（琥珀即「需要注意」，与模块内陈旧/离线标注同一套颜色语言）。
  .to__trunc {
    color: var(--aa-warning-text);
    font-size: 12px;
  }

  // exec 命令行：一眼可读的一行（等宽 + 折行，长命令不横向溢出）。
  .to__cmd {
    margin: 0;
    color: var(--el-text-color-primary);
    font-family: var(--art-font-family-mono);
    font-size: 12px;
    line-height: 1.6;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }

  // 键值清单：标签列 + 值列（两列栅格，窄屏下标签也不被挤没）。
  .to__kv {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 2px 10px;
    margin: 0;
    align-items: baseline;
  }

  .to__key {
    color: var(--el-text-color-secondary);
    font-size: 12px;
    white-space: nowrap;

    // 注册表查不到的键：原样等宽（等宽即「这是原始码」的提示）。
    &.is-raw {
      font-family: var(--art-font-family-mono);
    }
  }

  .to__val {
    margin: 0;
    min-width: 0;
    color: var(--el-text-color-regular);
    font-family: var(--art-font-family-mono);
    font-size: 12px;
    line-height: 1.6;
    overflow-wrap: anywhere;
    white-space: pre-wrap;

    // 长值/嵌套（YAML 正文、env 列…）：等宽块 + 限高滚动（视口相对，无写死像素），
    // 可读可滚且不撑破展开行。
    &.is-block {
      max-height: 32vh;
      padding: 6px 8px;
      overflow: auto;
      border-radius: 4px;
      background: var(--el-fill-color-light);
    }
  }

  // 原文兜底块：同一套等宽块皮肤。
  .to__raw {
    max-height: 32vh;
    margin: 0;
    padding: 6px 8px;
    overflow: auto;
    border-radius: 4px;
    background: var(--el-fill-color-light);
    color: var(--el-text-color-regular);
    font-family: var(--art-font-family-mono);
    font-size: 12px;
    line-height: 1.6;
    overflow-wrap: anywhere;
    white-space: pre-wrap;
  }
</style>