<template>
  <!-- 单根（single-root 守卫扫描全模块的 .vue）：ElDialog 是唯一根（与
       pull-progress-dialog / action-confirm 同一挂法，原地渲染不搬 body）。 -->
  <ElDialog
    :model-value="modelValue"
    title="构建镜像"
    width="640px"
    :close-on-click-modal="false"
    :close-on-press-escape="phase !== 'building'"
    @update:model-value="onDialogVisible"
  >
    <!-- ── 输入态：tag / context / dockerfile / build-args ──
         确认档照 image:build 的注册表现状（confirm: 'none'）：点了直接派发 ——
         对话框本身就是一次明确的确认（填表 + 开始构建两步），协议也不要求
         confirm 值；主参数是一整张表单，不走 action-confirm 的输入档（那档只收
         单个参数）。 -->
    <div v-if="phase === 'input'" class="bp-input">
      <div class="bp-field">
        <label class="bp-field__label">目标镜像引用</label>
        <ElInput
          v-model="tagInput"
          placeholder="例如 registry.example.com/app:v1"
          :disabled="accepting"
          clearable
          @keyup.enter="startBuild"
        />
        <p v-if="tagErrorText" class="bp-field__error">{{ tagErrorText }}</p>
        <p class="bp-field__hint">构建产物的名字（仓库/名称:标签）；不带标签时服务端补 :latest。</p>
      </div>

      <div class="bp-field">
        <div class="bp-field__head">
          <label class="bp-field__label">构建上下文（tar 文件名）</label>
          <!-- 两种形态（P3·上传控件）：本机文件直传（② 端点，进度可见）vs 主机已有
               tar 的手填文件名（老路径保留）。上传在途时锁形态：切走会让「上传的
               结论」与「表单里的值」两份事实互相追尾。 -->
          <ElRadioGroup v-model="ctxMode" size="small" :disabled="accepting || ctxUploading">
            <ElRadioButton value="upload">上传文件</ElRadioButton>
            <ElRadioButton value="manual">主机已有文件</ElRadioButton>
          </ElRadioGroup>
        </div>

        <!-- 手动形态：主机上已放好 tar 的场景（原路径原样）。 -->
        <template v-if="ctxMode === 'manual'">
          <ElInput
            v-model="contextInput"
            placeholder="例如 app.tar"
            :disabled="accepting"
            clearable
            @keyup.enter="startBuild"
          />
          <p v-if="contextErrorText" class="bp-field__error">{{ contextErrorText }}</p>
          <p class="bp-field__hint"
            >只填文件名（tar / tar.gz / tgz，≤512MB），需已放在该主机的 agent 下载目录。</p
          >
        </template>

        <!-- 上传形态：选文件 → 预检（后缀/512MB/gzip 魔数）→ 流式上传（进度条）→
             成功把服务端产物名填进 context。 -->
        <template v-else>
          <!-- 藏起来的原生 file input：ElUpload 自带一套上传语义（列表/自动分批），
               与「一个文件、一条流、自管进度」的诉求不合 —— 原生 input + 自管按钮
               每一步都可断言。 -->
          <input
            ref="fileInputEl"
            type="file"
            class="bp-file-hidden"
            :accept="BUILD_CONTEXT_ACCEPT"
            @change="onFilePicked"
          />

          <!-- 空闲/失败：入口按钮 + 预检结论（失败句就地给，不上传才知道便宜）。
               失败态的入口措辞换成「重新选择」—— 重试的心智就是「换个/重选文件」。 -->
          <template v-if="ctxUploadPhase !== 'uploading' && ctxUploadPhase !== 'done'">
            <div class="bp-upload-entry">
              <ElButton size="small" :disabled="accepting" @click="onEntryClick">
                {{ ctxUploadPhase === 'failed' ? '重新选择' : '选择文件…' }}
              </ElButton>
              <span class="bp-upload-entry__name">{{ ctxFileName }}</span>
            </div>
            <p v-if="ctxUploadError" class="bp-field__error">{{ ctxUploadError }}</p>
            <p class="bp-field__hint"
              >选择本机的 gzip 压缩 tar（≤512MB），上传后自动填入上下文文件名。</p
            >
          </template>

          <!-- 上传在途：文件名 + 进度条（XHR 的 upload 进度，fetch 没有这个事件）。 -->
          <template v-else-if="ctxUploadPhase === 'uploading'">
            <p class="bp-upload-file">{{ ctxFileName }}</p>
            <ElProgress
              :percentage="ctxUploadPercent"
              :stroke-width="8"
              :show-text="true"
              class="bp-upload-progress"
            />
          </template>

          <!-- 完成：只读展示「已上传：产物名」（值就是 context，改它只能清除重选或
               切回手动形态 —— 手动形态里它是可编辑的）。 -->
          <template v-else>
            <div class="bp-uploaded">
              <ArtSvgIcon icon="ri:checkbox-circle-line" class="bp-uploaded__icon" />
              <span>已上传：</span><code class="bp-uploaded__name">{{ trimmedContext }}</code>
            </div>
            <div class="bp-uploaded__ops">
              <ElButton size="small" text :disabled="accepting" @click="resetUpload">
                重新选择
              </ElButton>
              <ElButton size="small" text :disabled="accepting" @click="ctxMode = 'manual'">
                改回手动输入
              </ElButton>
            </div>
            <p class="bp-field__hint"
              >文件已传到该主机的 agent 下载目录，「开始构建」将直接使用它。</p
            >
          </template>
        </template>
      </div>

      <div class="bp-field">
        <label class="bp-field__label">Dockerfile 路径（可选）</label>
        <ElInput
          v-model="dockerfileInput"
          placeholder="例如 docker/Dockerfile（缺省 = 上下文根的 Dockerfile）"
          :disabled="accepting"
          clearable
          @keyup.enter="startBuild"
        />
        <p v-if="dockerfileErrorText" class="bp-field__error">{{ dockerfileErrorText }}</p>
        <p class="bp-field__hint">相对构建上下文的路径，各段不能是 .. 或反斜杠。</p>
      </div>

      <!-- build-args 键值编辑器：可空（缺省 = 不带 args 字段，载荷干净）；
           键校验用协议的标识符尺（bad key 当场显错，不送去吃 400）。 -->
      <div class="bp-field">
        <label class="bp-field__label">构建参数（可选）</label>
        <div v-for="(row, i) in argRows" :key="i" class="bp-arg">
          <ElInput
            v-model="row.key"
            class="bp-arg__key"
            placeholder="参数名"
            :disabled="accepting"
          />
          <ElInput
            v-model="row.value"
            class="bp-arg__value"
            placeholder="值"
            :disabled="accepting"
          />
          <ElButton size="small" text type="danger" :disabled="accepting" @click="removeArg(i)">
            移除
          </ElButton>
        </div>
        <p v-if="argErrorText" class="bp-field__error">{{ argErrorText }}</p>
        <ElButton
          v-if="argRows.length < MAX_BUILD_ARGS"
          size="small"
          text
          type="primary"
          :disabled="accepting"
          @click="addArg"
        >
          ＋添加参数
        </ElButton>
        <!-- 协议钉的披露义务：build-arg 会永驻镜像历史（docker history 可见）——
             这不是提示性的「小心」，是「这条输入通道不该承载秘密」的结论。 -->
        <p class="bp-field__hint"
          >构建参数会写进镜像历史（docker history 可见），不要填入密码等秘密。</p
        >
      </div>

      <!-- 受理期错误（403/409/400…）：分类句来自 classifyAcceptError（服务端 msg
           优先）；留在输入态可改可重试，不弹 toast —— 对话框还开着，结论就地给。 -->
      <p v-if="acceptError" class="bp-field__error">{{ acceptError }}</p>
    </div>

    <!-- ── 进度态：构建播报（文本行按序 + 步骤行折叠）── -->
    <div v-else-if="phase === 'building'" class="bp-progress">
      <div class="bp-progress__ref">{{ buildTarget }}</div>
      <p v-if="feedView.droppedLines > 0" class="bp-progress__drop">
        输出较长，已丢弃前 {{ feedView.droppedLines }} 行（只保留最近一段）
      </p>

      <ul ref="linesEl" class="bp-lines">
        <li v-for="l in feedView.lines" :key="l.key" class="bp-line" :class="{ 'is-step': l.step }">
          <!-- 步骤行带 id 前缀（与 CLI 的「Step 2/4 : RUN …」同观感）；文本行
               （buildkit "#1 …" / 经典输出）原文照登 —— 等宽才读得出参数边界。 -->
          <span v-if="l.id" class="bp-line__id">{{ l.id }}</span>
          <span class="bp-line__text">{{ l.text }}</span>
        </li>
      </ul>
      <p v-if="feedView.lines.length === 0" class="bp-progress__empty">正在等待构建输出…</p>

      <!-- 收尾提示：三态（用户取消 / 流接入失败 / 流中途断开）+ eof 后等 result，
           各自说清「接下来等什么」，不让对话框看起来像卡死（与拉取同款）。 -->
      <p v-if="waitingHint" class="bp-progress__hint">{{ waitingHint }}</p>
    </div>

    <!-- ── 终态：结论句（成功 = 镜像引用 + 耗时；失败 = 结论句原文）── -->
    <div v-else class="bp-result" :class="resultOk ? 'is-ok' : 'is-fail'">
      <ArtSvgIcon :icon="resultIcon" class="bp-result__icon" />
      <span class="bp-result__text">{{ resultText }}</span>
    </div>

    <template #footer>
      <template v-if="phase === 'input'">
        <ElButton :disabled="accepting" @click="requestClose">关闭</ElButton>
        <ElButton type="primary" :disabled="!canStart" :loading="accepting" @click="startBuild">
          开始构建
        </ElButton>
      </template>
      <!-- 构建在途只有一条出路：取消（断流 → 服务端终止构建）。ESC 与遮罩点击此时
           都被关掉（action-confirm 的 loading 纪律）：误触不该杀掉一场进行中的构建。 -->
      <ElButton v-else-if="phase === 'building'" :disabled="canceled" @click="cancelBuild">
        {{ canceled ? '已取消' : '取消构建' }}
      </ElButton>
      <ElButton v-else type="primary" @click="requestClose">关闭</ElButton>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  /**
   * 构建进度对话框（P2 分发闭环前端半边）：镜像页底栏「构建镜像…」的输入面 +
   * 逐行构建播报 + 取消，取代黑盒等待。
   *
   * 双通道并行（后端契约，与 4b 拉取逐条同源）：
   *   - 进度流 GET cmds/:ref/build —— 指令 **pending 期间即可接入**（会话由 core
   *     受理时按句柄 build_<ref> 预登记），构建全程逐行可见；eof 挂在最后一条
   *     终态记录行上；
   *   - result 轮询照旧（cmds/:ref）—— 终态结论句由它收尾，且 eof 先于 result
   *     终态（agent 钉住的时序）。
   * 收尾口径：result 到终态时以它为准（结论句原文）；流 eof 之后 result 迟迟不落
   * 时退回流的终态项 —— 双确认但不互等致死。
   *
   * 断流的语义重量（与拉取同一条契约）：**客户端断开 = 服务端取消这场构建**。
   * 取消按钮、关对话框、卸载都走同一条 abort。
   *
   * 为什么是独立组件而不是泛化 pull-progress-dialog：输入面结构性不同（拉取是
   * 一个引用 + 凭据，构建是 tag/上下文/Dockerfile/参数一张表单），进度面也不同
   * （层表 + 字节条 vs 文本播报）—— 能共享的（解析/折叠/收尾纪律）都在纯逻辑层
   * 复用（utils/build-push.ts、utils/pull.ts 的同款时序），生命周期骨架照 4b 克隆
   * 一份（与 task-pull-progress 的取舍同源：不为了「一份基类」去动已验证的组件）。
   *
   * 生命周期纪律照 pull-progress-dialog：序号 + AbortController，关对话框/重新
   * 打开让在飞的「流 + 轮询」双双失效；主机切换由页面关掉本对话框（resetForHostSwitch），
   * 组件内再把 hostId 在受理时钉死一道（双保险）。
   */
  import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
  import {
    ElButton,
    ElDialog,
    ElInput,
    ElProgress,
    ElRadioButton,
    ElRadioGroup
  } from 'element-plus'
  import { formatByUnit } from '@/modules/device/utils/display'
  import {
    fetchDockerCmdResult,
    openDockerBuildStream,
    sendDockerCmd,
    uploadDockerBuildContext
  } from '../api'
  import { classifyAcceptError } from '../composables/useDockerCmds'
  import { pollDelay } from '../utils/cmd'
  import { isValidImageRef } from '../utils/pull'
  import {
    createBuildFeed,
    isValidBuildArgKey,
    isValidBuildContextFilename,
    isValidBuildSubpath,
    MAX_BUILD_ARG_VALUE_BYTES,
    MAX_BUILD_ARGS,
    MAX_BUILD_CONTEXT_BYTES,
    BUILD_CONTEXT_ACCEPT,
    hasBuildContextSuffix,
    isGzipFile,
    type BuildFeed,
    type BuildFeedLine,
    type BuildTerminal
  } from '../utils/build-push'

  defineOptions({ name: 'DockerBuildProgressDialog' })

  const props = defineProps<{
    modelValue: boolean
    /** 构建目标主机（受理时钉死：一次指令只属于受理它的那台主机）。 */
    hostId: string
    /** 成功关闭后的重拉（页面的 refresh；双次 —— 立即 + 1.5s 落定，新镜像要进列表）。 */
    refresh?: () => void | Promise<void>
  }>()

  const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

  type Phase = 'input' | 'building' | 'result'

  interface FeedView {
    lines: BuildFeedLine[]
    droppedLines: number
  }

  interface ResultState {
    ok: boolean
    text: string
  }

  /** build-args 编辑器的一行（键值都允许先空着 —— 全空的行在提交时跳过）。 */
  interface ArgRow {
    key: string
    value: string
  }

  const phase = ref<Phase>('input')
  const tagInput = ref('')
  const contextInput = ref('')
  const dockerfileInput = ref('')
  const argRows = ref<ArgRow[]>([])
  const acceptError = ref('')
  const accepting = ref(false)

  // ── 构建上下文的上传形态（P3·② 端点的消费面）────────────────────────
  // 与手填形态共用 contextInput 这一个值（image:build 的 options.context 只认
  // 它）；上传通道的差异全部收在下面这组状态里，模板按 ctxMode 分叉。

  /** 上下文来源：manual = 手填主机上已有 tar 的文件名（老路径）；upload = 本机直传。 */
  const ctxMode = ref<'manual' | 'upload'>('manual')
  /** 上传段状态机：idle（未选/已清）→ uploading → done（产物名已回填）/
   *  failed（预检拒或服务端拒 —— 结论句就地给，重选即重试）。 */
  const ctxUploadPhase = ref<'idle' | 'uploading' | 'done' | 'failed'>('idle')
  /** 本地选中的文件名（选择/在途/失败时回显；成功后回显切到服务端产物名）。 */
  const ctxFileName = ref('')
  const ctxUploadPercent = ref(0)
  /** 预检与上传的失败结论句（与受理错误同一条「对话框还开着，结论就地给」纪律）。 */
  const ctxUploadError = ref('')
  const ctxUploading = computed(() => ctxUploadPhase.value === 'uploading')
  /** 藏起来的原生文件选择器（模板见 file input 的注释）。 */
  const fileInputEl = ref<HTMLInputElement | null>(null)
  /** 上传段生命周期序号：关对话框/重开/清除重选让在飞的「预检 await + 上传」失效。 */
  let ctxUploadSeq = 0
  /** 在途上传的断流柄：断开 = 中止请求（服务端会向 agent 发中止帧清理半成品）。 */
  let uploadAbort: AbortController | null = null

  const feedView = ref<FeedView>({ lines: [], droppedLines: 0 })
  const canceled = ref(false)
  const streamEnded = ref(false)
  /** '' = 流还连着；'open' = 接入失败（构建仍在服务端跑）；'broken' = 连上后断开（断开即取消）。 */
  const streamFailed = ref<'' | 'open' | 'broken'>('')
  const streamFailText = ref('')
  const resultState = ref<ResultState | null>(null)
  /** 进度态头部的目标引用（受理时钉下；模板要显示，故是 ref）。 */
  const buildTarget = ref('')
  /** 播报容器（自动跟随滚动到最新行 —— 构建是「直播」，停在首屏就是看旧闻）。 */
  const linesEl = ref<HTMLUListElement | null>(null)

  /** 生命周期序号：关闭/重开让在飞的「流 + 轮询」双双失效（照 pull-progress-dialog）。 */
  let buildSeq = 0
  let streamAbort: AbortController | null = null
  /** 流的终态项（done/error 那一行；取消路径上服务端不发）。 */
  let streamTerminal: BuildTerminal | null = null
  let startedAt = 0
  let endedAt = 0
  /** 双次重拉只补一次的闸（关闭路径可能被走两遍：页脚按钮 + prop 回写）。 */
  let refreshedAfterSuccess = false

  /** 流收口后 result 轮询的兜底上限（对齐 useDockerCmds 的 20 次口径）。 */
  const MAX_RESULT_POLLS = 20

  // ── 输入态 ──

  /** 与 pull 对话框 / 注册表输入档同一句人话（同一把协议尺 dockerImageRefRe）。 */
  const IMAGE_REF_ERROR = '镜像引用不合法：以字母或数字开头，仅可包含字母、数字与 / . _ : @ -'

  const trimmedTag = computed(() => tagInput.value.trim())
  const trimmedContext = computed(() => contextInput.value.trim())
  const trimmedDockerfile = computed(() => dockerfileInput.value.trim())

  /** 校验是实时的（非空才显错）：空串不该挨骂，留白首尾在提交时裁掉。 */
  const tagErrorText = computed(() => {
    const v = trimmedTag.value
    if (v === '') return ''
    return isValidImageRef(v) ? '' : IMAGE_REF_ERROR
  })

  /**
   * 上下文文件名前端先挡一道（协议 IsDockerBuildContextFilename 的正则镜像）——
   * 与 image:load「只拦空值」的分工见 utils/build-push.ts 的口径说明：构建失败的
   * 等待是分钟级，一眼就地改便宜得多。
   */
  const CONTEXT_ERROR = '文件名不合法：只填 tar / tar.gz / tgz 的文件名（不带路径）'

  const contextErrorText = computed(() => {
    const v = trimmedContext.value
    if (v === '') return ''
    return isValidBuildContextFilename(v) ? '' : CONTEXT_ERROR
  })

  const DOCKERFILE_ERROR = '路径不合法：相对上下文的路径，各段不能是 … 或 . 、不能有反斜杠'

  const dockerfileErrorText = computed(() => {
    const v = trimmedDockerfile.value
    if (v === '') return ''
    return isValidBuildSubpath(v) ? '' : DOCKERFILE_ERROR
  })

  /** 参数行的错误句（第一个出问题的行）：全空行不算错（提交时跳过）。 */
  const argErrorText = computed(() => {
    for (const row of argRows.value) {
      const k = row.key.trim()
      const v = row.value.trim()
      if (k === '' && v === '') continue
      if (!isValidBuildArgKey(k)) {
        return '参数名不合法：以字母或下划线开头，仅可包含字母、数字与下划线'
      }
      if (v.length > MAX_BUILD_ARG_VALUE_BYTES) {
        return `参数「${k}」的值超过 ${MAX_BUILD_ARG_VALUE_BYTES} 字节上限`
      }
    }
    return ''
  })

  const canStart = computed(
    () =>
      trimmedTag.value !== '' &&
      tagErrorText.value === '' &&
      trimmedContext.value !== '' &&
      contextErrorText.value === '' &&
      dockerfileErrorText.value === '' &&
      argErrorText.value === '' &&
      // 上传在途不放行（此时 context 是空的本来也拦住了，这里把「为什么不能开始」
      // 的语义钉在状态机上，不靠「恰好为空」这个间接事实）。
      !ctxUploading.value &&
      !accepting.value
  )

  function addArg() {
    if (argRows.value.length >= MAX_BUILD_ARGS) return // 协议条目上限（32），表单侧就不放开
    argRows.value.push({ key: '', value: '' })
  }

  function removeArg(i: number) {
    argRows.value.splice(i, 1)
  }

  /** 把编辑器折成载荷的 args 映射（全空行跳过；键值都裁首尾空白）。 */
  function argsOfPayload(): Record<string, string> {
    const args: Record<string, string> = {}
    for (const row of argRows.value) {
      const k = row.key.trim()
      const v = row.value.trim()
      if (k === '') continue // 校验已保证「有值的行键必合法」，空键行 = 整行空
      args[k] = v
    }
    return args
  }

  // ── 上下文上传段（P3）：选文件 → 预检 → 流式上传 → 产物名回填 ──

  /**
   * 断开在途上传（幂等）：序号让在飞的「预检 await / 进度回调 / 上传 promise」
   * 失效，abort 中止请求本身。状态机与表单值的归零在 resetUpload（重开/重选场景
   * 才需要），这里只断流 —— 与 abortStream/cleanup 的分工同款。
   */
  function abortUpload(): void {
    ctxUploadSeq++
    uploadAbort?.abort()
    uploadAbort = null
  }

  /**
   * 清除上传段（「重新选择」与切形态共用）：状态机归零 + context 一并清。
   *
   * context 必须一起清：上传形态下它是上传通道的唯一产物，旧值残留会变成
   * 「界面上看不见、载荷里却发得出去」的暗事实。成功后的值要保留只能走
   * 「改回手动输入」（手动形态里它是可编辑的普通输入）。
   */
  function resetUpload(): void {
    abortUpload()
    ctxUploadPhase.value = 'idle'
    ctxUploadError.value = ''
    ctxFileName.value = ''
    ctxUploadPercent.value = 0
    contextInput.value = ''
  }

  // 切形态：切进上传 = 来源换成上传通道，手填值与旧上传状态一并清零（「显示的
  // 结论」与「将发送的值」不能是两份事实）；切回手动保留现值（上传成功后 =
  // 服务端产物名，可编辑可清空 —— 手动形态本来就是为「主机已有 tar」留的路）。
  watch(ctxMode, (mode) => {
    if (mode === 'upload') resetUpload()
  })

  function pickFile(): void {
    fileInputEl.value?.click()
  }

  /**
   * 入口按钮（空闲/失败两态共用）：失败态先清结论再开选择器 ——「重新选择」的
   * 心智是从头来，旧错误句不该跟着进下一轮（选完新文件本来也会清，这里让
   * 点击的瞬间就有反馈）。
   */
  function onEntryClick(): void {
    if (ctxUploadPhase.value === 'failed') {
      ctxUploadPhase.value = 'idle'
      ctxUploadError.value = ''
    }
    pickFile()
  }

  /** 预检失败的就地结论（状态机停在 failed，重选即重试 —— 与受理失败同款）。 */
  function failUpload(message: string): void {
    ctxUploadPhase.value = 'failed'
    ctxUploadError.value = message
  }

  /**
   * 选定文件：预检三连（后缀 / 512MB / gzip 魔数，全是毫秒级反馈）→ 通过即发
   * 起流式上传。预检是服务端同一把尺的前移，不是替它执法（跳过预检的路径 ——
   * 比如直接 POST —— 服务端照拒，只是反馈从毫秒级变成「传完才知道」）。
   */
  async function onFilePicked(e: Event): Promise<void> {
    const input = e.target as HTMLInputElement
    const file = input.files?.[0]
    // value 先清：否则第二次选同一个文件不触发 change（浏览器按值判变化）。
    input.value = ''
    if (!file || phase.value !== 'input' || accepting.value || ctxUploading.value) return
    const seq = ctxUploadSeq
    ctxFileName.value = file.name
    ctxUploadError.value = ''
    ctxUploadPhase.value = 'idle'

    if (!hasBuildContextSuffix(file.name)) {
      failUpload('只支持 tar / tar.gz / tgz 文件')
      return
    }
    if (file.size > MAX_BUILD_CONTEXT_BYTES) {
      failUpload(`文件超过 ${MAX_BUILD_CONTEXT_BYTES / (1024 * 1024)}MB 上限`)
      return
    }
    // gzip 魔数（异步读切片的头部两字节）：await 之后对话框可能已被关掉/重置，
    // 序号守卫让这次选择静默作废（与构建流的 seq 同一条纪律）。
    if (!(await isGzipFile(file))) {
      if (seq !== ctxUploadSeq) return
      failUpload('内容不是 gzip 压缩的 tar 归档')
      return
    }
    if (seq !== ctxUploadSeq) return

    // 发起上传：hostId 在此钉死（与 startBuild 同理由 —— 一次上传只属于受理它
    // 的那台主机，切机后由页面关掉本对话框，这里钉死是兜那条缝）。
    ctxUploadPhase.value = 'uploading'
    ctxUploadPercent.value = 0
    const controller = new AbortController()
    uploadAbort = controller
    try {
      const resp = await uploadDockerBuildContext(
        props.hostId,
        file,
        (percent) => {
          if (seq === ctxUploadSeq) ctxUploadPercent.value = percent
        },
        controller.signal
      )
      if (seq !== ctxUploadSeq) return
      // 成功：产物名回填 context（它就是 image:build 的 options.context 要填的
      // 值 —— 服务端由会话号推导，形态过协议的文件名白名单，校验不会挡它）。
      contextInput.value = resp.filename
      ctxUploadPhase.value = 'done'
    } catch (err) {
      // 收口路径（关对话框/清除重选）上的 abort 不是故障：请求是被自己掐断的。
      if (seq !== ctxUploadSeq) return
      ctxUploadPhase.value = 'failed'
      // 服务端结论句优先（离线 503 / 超限 400 的原文比前端编的准），分类兜底
      // 同 classifyAcceptError 的口径。
      ctxUploadError.value = classifyAcceptError(err).message
    } finally {
      if (uploadAbort === controller) uploadAbort = null
    }
  }

  // ── 进度态的派生展示 ──

  /** 收尾提示：把「还在等什么」说清楚（三态收口 + eof 后等 result），与拉取同款。 */
  const waitingHint = computed(() => {
    if (phase.value !== 'building') return ''
    if (canceled.value) return '已取消，正在等待指令收尾…'
    if (streamFailed.value === 'open') {
      return `进度流未能建立（${streamFailText.value}），构建仍在进行，等待结果…`
    }
    if (streamFailed.value === 'broken') {
      return streamFailText.value
        ? `进度流已断开（${streamFailText.value}），断开即取消构建，正在等待收尾…`
        : '进度流已断开，断开即取消构建，正在等待收尾…'
    }
    if (streamEnded.value) return '构建输出已全部到达，正在等待指令结果…'
    return ''
  })

  // ── 终态的派生展示 ──

  const resultOk = computed(() => resultState.value?.ok ?? false)
  const resultText = computed(() => resultState.value?.text ?? '')
  const resultIcon = computed(() =>
    resultOk.value ? 'ri:checkbox-circle-line' : 'ri:close-circle-line'
  )

  // ── 小工具（文案与错误归类，照 pull-progress-dialog 的同名函数口径） ──

  const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 流端点接入失败的结论句（状态码是排障线索，不进页面）。 */
  function streamOpenConclusion(status: number): string {
    if (status === 401) return '登录状态已失效'
    if (status === 403) return '没有接入该进度流的权限'
    if (status === 404) return '该指令已不存在或会话已过期'
    if (status === 409) return '该进度会话已有连接'
    return '进度流未能建立'
  }

  // ── 发起（受理 → 双通道并行） ──

  async function startBuild(): Promise<void> {
    if (!canStart.value) return
    const tag = trimmedTag.value
    const context = trimmedContext.value
    const dockerfile = trimmedDockerfile.value
    const args = argsOfPayload()
    // hostId 在入口钉死（与 useDockerCmds 同理由）：构建在途时切了主机，轮询跟着
    // 新主机走会拿老 ref 查新记录。切机时页面会关掉本对话框，这里钉死是兜那条缝。
    const hostId = props.hostId
    accepting.value = true
    acceptError.value = ''
    let cmdRef: string
    try {
      const accepted = await sendDockerCmd(hostId, {
        action: 'image:build',
        // build 没有 target（协议 validateDockerBuild 显式拒绝）：语义都在
        // context/tag 里，与 container:create 同型。可选项缺省时**不发字段**
        //（协议对空串与缺省同判非法的口径，宁缺勿滥 —— dockerfile 缺省由
        // agent 补 Dockerfile，args 空映射不带）。
        options: {
          tag,
          context,
          ...(dockerfile !== '' ? { dockerfile } : {}),
          ...(Object.keys(args).length > 0 ? { args } : {})
        }
      })
      cmdRef = accepted.ref
    } catch (e) {
      // 受理失败留在输入态（输入保留，可改可重试）：403/409/400 的结论句由
      // classifyAcceptError 分类（服务端 msg 优先）。
      acceptError.value = classifyAcceptError(e).message
      return
    } finally {
      accepting.value = false
    }
    buildTarget.value = tag
    startedAt = Date.now()
    streamTerminal = null
    phase.value = 'building'
    const seq = ++buildSeq
    // 双通道并行：流（pending 期间即可接入）+ 轮询（终态结论句）。
    void runStreamLoop(seq, hostId, cmdRef)
    void runPollLoop(seq, hostId, cmdRef)
  }

  // ── 通道一：进度流 ──

  /** 把折叠器的当前快照换进响应式视图（换数组引用驱动重渲染）。 */
  function syncFeed(feed: BuildFeed): void {
    feedView.value = { lines: feed.lines.slice(), droppedLines: feed.droppedLines }
    // 播报跟随到最新行（构建是直播，不是回首屏看旧闻）；容器还没挂/已卸载时跳过。
    void nextTick(() => {
      const el = linesEl.value
      if (el) el.scrollTop = el.scrollHeight
    })
  }

  async function runStreamLoop(seq: number, hostId: string, cmdRef: string): Promise<void> {
    const controller = new AbortController()
    streamAbort = controller
    try {
      const res = await openDockerBuildStream(hostId, cmdRef, controller.signal)
      if (seq !== buildSeq) return
      if (!res.ok || !res.body) {
        // 接入失败（401/403/404/409…）：这条连接没建立过，**不会**触发服务端取消 ——
        // 构建仍在跑，降级为「无进度、等结果」，不中止流程。
        streamFailed.value = 'open'
        streamFailText.value = streamOpenConclusion(res.status)
        return
      }
      const feed = createBuildFeed()
      const reader = res.body.getReader()
      const dec = new TextDecoder('utf-8')
      for (;;) {
        const { done, value } = await reader.read()
        if (seq !== buildSeq) return
        if (done) break
        feed.pushRaw(dec.decode(value, { stream: true }))
        syncFeed(feed)
        if (feed.eof) break
      }
      if (seq !== buildSeq) return
      // 收尾：把解码器里的残留字节冲进折叠器（终态行可能正好被切在 chunk 尾）。
      feed.pushRaw(dec.decode())
      syncFeed(feed)
      if (feed.eof) {
        streamTerminal = feed.terminal
        streamEnded.value = true
      } else {
        // 读尽但没见 eof：网络层收口、应用层没收官 —— 视同断流（断开即取消）。
        streamFailed.value = 'broken'
        streamFailText.value = ''
      }
    } catch (e) {
      if (seq !== buildSeq) return
      // 主动断开（取消/收尾/关对话框）不是故障：流是被自己掐断的。
      if ((e as { name?: string })?.name === 'AbortError') return
      streamFailed.value = 'broken'
      streamFailText.value = errMsg(e, '进度流连接中断')
    } finally {
      if (streamAbort === controller) streamAbort = null
    }
  }

  // ── 通道二：result 轮询（终态收尾） ──

  async function runPollLoop(seq: number, hostId: string, cmdRef: string): Promise<void> {
    let attempt = 0
    /** 兜底上限的起算点：流收口之后才起算 —— 流还活着时轮询**不设上限**（30 分钟档
        的构建里，播报行就是「还活着」的证据；useDockerCmds 的固定 20 次上限是给
        黑盒等待的，这里流把「活着」的举证接过去了）。 */
    let deadlineArmedAt: number | null = null
    for (;;) {
      if (seq !== buildSeq) return
      let res: Awaited<ReturnType<typeof fetchDockerCmdResult>> | undefined
      try {
        res = await fetchDockerCmdResult(hostId, cmdRef)
      } catch {
        res = undefined // 单次网络失败不当终态：流通道可能还活着，下一轮自愈
      }
      if (seq !== buildSeq) return
      if (res && res.status !== 'pending') {
        finalize(seq, res.status === 'succeeded', res.error ?? '')
        return
      }
      attempt++
      if (deadlineArmedAt === null && resultDeadlineArmed()) deadlineArmedAt = attempt
      if (deadlineArmedAt !== null && attempt - deadlineArmedAt >= MAX_RESULT_POLLS) {
        finalizeFallback(seq)
        return
      }
      await sleep(pollDelay(attempt - 1))
    }
  }

  /** result 轮询是否该起兜底上限：eof / 断流 / 用户取消之后，result 按时序应很快
      落定（agent 钉了 eof 先于 result），等不到就是记录异常，不该无限等。 */
  function resultDeadlineArmed(): boolean {
    return streamEnded.value || streamFailed.value !== '' || canceled.value
  }

  /** result 终态收尾（权威通道）：结论句原文 + 断掉还挂着的流。 */
  function finalize(seq: number, ok: boolean, error: string): void {
    if (seq !== buildSeq) return
    endedAt = Date.now()
    // result 已终态：流若还挂着（时序兜底下可能没走完）就断掉 —— 会话已收口，
    // 此时的 cancel 无副作用。
    abortStream()
    if (ok) {
      setResult(true, `已构建 ${buildTarget.value}（耗时 ${durationText()}）`)
    } else {
      setResult(false, error || '构建镜像失败')
    }
  }

  /** result 等不到（记录异常/网络断）的退路：结论退回流的终态项；取消路径上服务端
      不发终态项，按「已取消」措辞；两头都没有时只说「未能确认」—— 不编造结论。 */
  function finalizeFallback(seq: number): void {
    if (seq !== buildSeq) return
    endedAt = Date.now()
    if (canceled.value) {
      setResult(false, '构建已取消')
    } else if (streamTerminal) {
      if (streamTerminal.ok) {
        setResult(true, `已构建 ${buildTarget.value}（耗时 ${durationText()}）`)
      } else {
        setResult(false, streamTerminal.error || '构建镜像失败')
      }
    } else {
      setResult(false, '构建结果未能确认，请稍后刷新镜像列表')
    }
  }

  function setResult(ok: boolean, text: string): void {
    resultState.value = { ok, text }
    phase.value = 'result'
  }

  function durationText(): string {
    const sec = Math.max(0, Math.round((endedAt - startedAt) / 1000))
    return formatByUnit('s', sec)
  }

  // ── 取消 / 关闭 / 收口 ──

  function cancelBuild(): void {
    if (phase.value !== 'building' || canceled.value) return
    canceled.value = true
    // 断流即取消（端点契约：客户端断开 → 服务端向 agent 下发 cancel 终止构建）。
    // result 轮询继续：结论句「构建已取消」由服务端落进 result，页面前端不代答。
    abortStream()
  }

  /** 断流（幂等：没有在飞的流就不扰动）。 */
  function abortStream(): void {
    streamAbort?.abort()
    streamAbort = null
  }

  /** 收口：序号让在飞的流与轮询失效 + 断流（= 服务端取消构建）+ 断开在途上传。幂等。 */
  function cleanup(): void {
    buildSeq++
    abortStream()
    abortUpload()
  }

  /** 关对话框的完整收口（幂等）：清理 + 成功后的双次重拉。 */
  function cleanupAndFinish(): void {
    cleanup()
    maybeDoubleRefresh()
  }

  /** 成功关闭后的双次重拉（useDockerCmds 的落定纪律，1.5s）：构建成功会**新增**
      一枚镜像，列表必须重拉才能看到它 —— 与拉取同一条「成功后世界变了」的理由。 */
  function maybeDoubleRefresh(): void {
    if (!resultState.value?.ok || refreshedAfterSuccess) return
    refreshedAfterSuccess = true
    safeRefresh()
    setTimeout(safeRefresh, 1500)
  }

  function safeRefresh(): void {
    try {
      Promise.resolve(props.refresh?.()).catch(() => {
        /* 静默：展示层动作不拖垮结论 */
      })
    } catch {
      /* 同上 */
    }
  }

  /** 页脚按钮的关闭路径（与 X/ESC/父组件置 false 同一条收口）。 */
  function requestClose(): void {
    cleanupAndFinish()
    emit('update:modelValue', false)
  }

  /** ElDialog 自己的关闭路径（X 按钮）：除转发给父组件外当场收口 —— 父组件没接
      v-model 时 prop 不会变，watch 不触发，流就漏了。 */
  function onDialogVisible(v: boolean): void {
    if (!v) cleanupAndFinish()
    emit('update:modelValue', v)
  }

  // 父组件置 false（主机切换 resetForHostSwitch / 页面级收口）走 watch；置 true
  //（打开）时整表重置 —— 上一次构建的任何残留（播报表/结论/刷新标记/表单）不进
  // 新一场。immediate：「带着打开态挂载」（测试/将来别的入口直接传 true）与
  //「先挂载再打开」走同一条路。
  watch(
    () => props.modelValue,
    (v, old) => {
      if (v) {
        resetAll()
        return
      }
      if (old) cleanupAndFinish()
    },
    { immediate: true }
  )

  function resetAll(): void {
    cleanup()
    phase.value = 'input'
    tagInput.value = ''
    contextInput.value = ''
    dockerfileInput.value = ''
    argRows.value = []
    acceptError.value = ''
    accepting.value = false
    // 上传段整段归零（含 context 与 abort —— resetUpload 里会再清一次 context，
    // 幂等无害）：上一次会话的产物名/进度/失败句不进新一场。
    ctxMode.value = 'manual'
    resetUpload()
    feedView.value = { lines: [], droppedLines: 0 }
    canceled.value = false
    streamEnded.value = false
    streamFailed.value = ''
    streamFailText.value = ''
    resultState.value = null
    buildTarget.value = ''
    streamTerminal = null
    startedAt = 0
    endedAt = 0
    refreshedAfterSuccess = false
  }

  // 卸载只收口不重拉：页面都没了，重拉的落点不存在（页脚关闭路径已补过重拉）。
  onBeforeUnmount(cleanup)
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use '../views/overview-tokens' as t;

  // 「取消」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  // 输入态：一张四字段表单（标签在输入框上方 —— 字段多了，与拉取的单列无标签
  // 形态分道；就地结论（错误句/提示句）都是对话框内的结论，不放 toast）。
  .bp-input {
    padding-bottom: 4px;
  }

  .bp-field {
    margin-bottom: 12px;

    &__label {
      display: block;
      margin-bottom: 6px;
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    &__error,
    &__hint {
      margin: 6px 0 0;
      font-size: 12px;
      line-height: 1.6;
    }

    &__error {
      color: var(--el-color-danger);
    }

    &__hint {
      color: var(--el-text-color-secondary);
    }
  }

  // 上下文字段头：标签与形态切换（radio）同行 —— 手填/上传是同一份语义的两种
  // 来源，切换就近放在标签旁；标签自己的下边距在这里由头部行接管（不然 radio
  // 会被顶出基线）。
  .bp-field__head {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 6px;

    .bp-field__label {
      margin-bottom: 0;
    }
  }

  // 原生文件选择器常驻 DOM（要它可被触发/断言），视觉上完全让位给自管按钮。
  .bp-file-hidden {
    display: none;
  }

  // 上传入口：按钮 + 已选文件名（文件名是「选了什么」的回执，弱化展示）。
  .bp-upload-entry {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;

    &__name {
      font-size: 12px;
      color: var(--el-text-color-secondary);
      word-break: break-all;
    }
  }

  // 在途：文件名（正文色）+ 进度条（传输进度是这段唯一的事实）。
  .bp-upload-file {
    margin: 0;
    font-size: 13px;
    color: var(--el-text-color-regular);
    word-break: break-all;
  }

  .bp-upload-progress {
    margin-top: 8px;
    margin-bottom: 2px;
  }

  // 完成：结论行（成功色图标 + 产物名等宽 —— 它是要被填进指令的标识符，等宽
  // 才读得出会话号段），两个次级动作以 text 按钮就近给出。
  .bp-uploaded {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    font-size: 13px;

    &__icon {
      flex: none;
      font-size: 16px;
      color: var(--el-color-success);
    }

    &__name {
      font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
      word-break: break-all;
    }

    &__ops {
      display: flex;
      gap: 4px;
      margin-top: 2px;
    }
  }

  // build-args 编辑器：一行 = 键（窄，标识符）+ 值（宽）+ 移除；行间 6px 比字段间
  // 紧 —— 行是同一组语义（一个参数），字段是不同语义。
  .bp-arg {
    display: flex;
    gap: 8px;
    align-items: center;
    margin-bottom: 6px;

    &__key {
      flex: 0 0 34%;
    }

    &__value {
      flex: 1;
    }
  }

  // 进度态头部：目标引用是主信息（加粗），丢弃行数提示是次级（弱化色）。
  .bp-progress {
    &__ref {
      color: var(--el-text-color-primary);
      font-size: 14px;
      font-weight: 600;
      word-break: break-all;
    }

    &__drop,
    &__empty,
    &__hint {
      margin: 6px 0 0;
      font-size: 12px;
      line-height: 1.6;
      color: var(--el-text-color-secondary);
    }
  }

  // 构建播报：限高滚动的等宽清单（构建输出是「代码的执行记录」，等宽才读得出
  // 步骤与参数边界；步骤行的 id 前缀弱化 —— 它是句柄，正文在后面）。
  .bp-lines {
    max-height: 300px;
    margin: 10px 0 0;
    padding: 8px 10px;
    list-style: none;
    overflow-y: auto;
    border-radius: 4px;
    background: var(--el-fill-color-light);
  }

  .bp-line {
    display: flex;
    gap: 8px;
    font-size: 12px;
    line-height: 1.7;
    font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
    word-break: break-all;

    &__id {
      flex: none;
      color: var(--el-text-color-secondary);
      white-space: nowrap;
    }

    &__text {
      min-width: 0;
      color: var(--el-text-color-regular);
    }

    // 步骤行与文本行同款渲染（CLI 的构建输出也不区分字号），只给 id 前缀弱化色。
  }

  // 终态：结论句是这一屏唯一的信息（与拉取对话框同款形态）。
  .bp-result {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    padding: 4px 0 8px;

    &__icon {
      flex: none;
      margin-top: 2px;
      font-size: 18px;
    }

    &__text {
      color: var(--el-text-color-primary);
      font-size: 14px;
      line-height: 1.6;
      word-break: break-all;
    }

    &.is-ok &__icon {
      color: var(--el-color-success);
    }

    &.is-fail &__icon {
      color: var(--el-color-danger);
    }
  }

  // 平板竖屏以下（对话框占满宽度）：参数行的键列收窄，给值留正文宽度。
  @include respond-below('tablet') {
    .bp-arg__key {
      flex: 0 0 42%;
    }
  }
</style>
