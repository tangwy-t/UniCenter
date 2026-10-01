<template>
  <!-- 单根（single-root 守卫在库：ElDrawer 是唯一根；确认弹窗挂在它的默认插槽里，
       ElDialog 自己会 teleport 到 body，与 workload-drawer 同一挂法）。 -->
  <ElDrawer
    v-model="visibleModel"
    class="ccd-drawer"
    direction="rtl"
    append-to-body
    :size="drawerSize"
    :close-on-press-escape="!busy"
  >
    <template #header>
      <div class="ccd-head">
        <h3 class="ccd-head__title">创建容器</h3>
        <p class="ccd-head__sub">镜像须已在该主机本地；没有的话先到镜像页拉取</p>
      </div>
    </template>

    <!-- 表单体：六个分组自上而下按「必答 → 列表 → 高级」排布 —— 镜像与名称是
         创建的最小决定（协议必填只有 image），端口/变量/挂载是逐条加的清单，
         资源与网络是可留空的高级项。空组不折叠：看得见才有加第一行的入口。 -->
    <div class="ccd-form">
      <!-- ══ 基本组：主机 / 镜像 / 名称 / 重启策略 / 创建后启动 ══ -->
      <section class="ccd-group">
        <div class="ccd-group__title">基本</div>

        <label class="ccd-field">
          <span class="ccd-field__label">主机</span>
          <div class="ccd-field__control">
            <ElSelect
              v-model="formHostId"
              class="ccd-field__input"
              :disabled="dockerOkHosts.length < 1"
              placeholder="选择主机"
              @change="onHostChange"
            >
              <ElOption
                v-for="h in dockerOkHosts"
                :key="h.id"
                :label="hostLabel(h)"
                :value="h.id"
              />
            </ElSelect>
            <span v-if="!dockerOkHosts.length" class="ccd-field__hint">
              没有可管理 Docker 的主机
            </span>
          </div>
        </label>

        <label class="ccd-field">
          <span class="ccd-field__label">镜像</span>
          <div class="ccd-field__control">
            <!-- 可搜索 + 允许输入任意合法引用（digest/无标签镜像只能手填）；
                 选项来自该主机的本地镜像清单 —— create 不自动拉取，选了不在本地的
                 引用必失败，下面那条引导就是替 agent 提前说这句结论。 -->
            <ElSelect
              v-model="form.image"
              class="ccd-field__input"
              filterable
              allow-create
              default-first-option
              :placeholder="imagePlaceholder"
              :loading="stateLoading"
            >
              <ElOption
                v-for="o in imageOptions"
                :key="o.value"
                :label="o.label"
                :value="o.value"
              />
            </ElSelect>
            <span v-if="imageIssueText" class="ccd-field__issue">{{ imageIssueText }}</span>
            <span v-else-if="stateError" class="ccd-field__issue">
              读取该主机的镜像清单失败
              <a class="ccd-field__retry" @click.prevent="loadState">重试</a>
            </span>
            <span v-else-if="stateLoading" class="ccd-field__hint">正在读取该主机的镜像清单…</span>
          </div>
        </label>

        <label class="ccd-field">
          <span class="ccd-field__label">容器名</span>
          <div class="ccd-field__control">
            <ElInput
              v-model="form.name"
              class="ccd-field__input"
              placeholder="不填则由 Docker 自动命名"
              clearable
            />
            <span v-if="nameIssueText" class="ccd-field__issue">{{ nameIssueText }}</span>
          </div>
        </label>

        <div class="ccd-field">
          <span class="ccd-field__label">重启策略</span>
          <div class="ccd-field__control">
            <ElSelect v-model="form.restartPolicy" class="ccd-field__input">
              <ElOption label="不自动重启（缺省）" value="" />
              <ElOption
                v-for="p in RESTART_POLICY_OPTIONS"
                :key="p.value"
                :label="p.label"
                :value="p.value"
              />
            </ElSelect>
          </div>
        </div>

        <div class="ccd-field">
          <span class="ccd-field__label">创建后启动</span>
          <div class="ccd-field__control ccd-switch">
            <ElSwitch v-model="form.start" />
            <span class="ccd-field__hint">
              {{ form.start ? '创建完成后立即启动容器' : '仅创建不启动，稍后可在容器页启动' }}
            </span>
          </div>
        </div>
      </section>

      <!-- ══ 端口：宿主:容器 + 协议，加减行 ══ -->
      <section class="ccd-group">
        <div class="ccd-group__title">端口映射</div>
        <div class="ccd-group__hint">两端都须 1-65535（随机端口不支持：创建后无从知道去哪连）</div>
        <div v-for="(row, i) in form.ports" :key="`p${i}`" class="ccd-row">
          <ElInput
            v-model="row.host"
            class="ccd-row__input"
            placeholder="宿主端口"
            :class="{ 'is-invalid': issues.ports[i] !== '' }"
          />
          <ElInput
            v-model="row.container"
            class="ccd-row__input"
            placeholder="容器端口"
            :class="{ 'is-invalid': issues.ports[i] !== '' }"
          />
          <ElSelect v-model="row.proto" class="ccd-row__proto">
            <ElOption label="tcp" value="tcp" />
            <ElOption label="udp" value="udp" />
          </ElSelect>
          <ElButton
            size="small"
            text
            type="danger"
            title="删除这一行"
            @click="removeRow(form.ports, i)"
          >
            删除
          </ElButton>
          <div v-if="issues.ports[i]" class="ccd-row__issue">{{ issues.ports[i] }}</div>
        </div>
        <div class="ccd-group__ops">
          <ElButton
            size="small"
            text
            :disabled="form.ports.length >= MAX_CREATE_LIST_ITEMS"
            @click="addPortRow"
          >
            ＋ 添加端口
          </ElButton>
          <span v-if="issues.portsCount" class="ccd-field__issue">{{ issues.portsCount }}</span>
        </div>
      </section>

      <!-- ══ 环境变量：键/值两列，加减行 ══ -->
      <section class="ccd-group">
        <div class="ccd-group__title">环境变量</div>
        <div class="ccd-group__hint">键须以字母或下划线开头；值可为空</div>
        <div v-for="(row, i) in form.env" :key="`e${i}`" class="ccd-row">
          <ElInput
            v-model="row.key"
            class="ccd-row__input"
            placeholder="变量名"
            :class="{ 'is-invalid': issues.env[i] !== '' }"
          />
          <ElInput
            v-model="row.value"
            class="ccd-row__input ccd-row__input--wide"
            placeholder="值"
            :class="{ 'is-invalid': issues.env[i] !== '' }"
          />
          <ElButton
            size="small"
            text
            type="danger"
            title="删除这一行"
            @click="removeRow(form.env, i)"
          >
            删除
          </ElButton>
          <div v-if="issues.env[i]" class="ccd-row__issue">{{ issues.env[i] }}</div>
        </div>
        <div class="ccd-group__ops">
          <ElButton
            size="small"
            text
            :disabled="form.env.length >= MAX_CREATE_LIST_ITEMS"
            @click="addEnvRow"
          >
            ＋ 添加变量
          </ElButton>
          <span v-if="issues.envDuplicate" class="ccd-field__issue">{{ issues.envDuplicate }}</span>
          <span v-else-if="issues.envCount" class="ccd-field__issue">{{ issues.envCount }}</span>
        </div>
      </section>

      <!-- ══ 挂载：源（命名卷/宿主路径）+ 目的地 + 只读，加减行 ══ -->
      <section class="ccd-group">
        <div class="ccd-group__title">挂载</div>
        <div class="ccd-group__hint"
          >源可以是命名卷或以 / 开头的宿主路径；目的地是容器内绝对路径</div
        >
        <div v-for="(row, i) in form.mounts" :key="`m${i}`" class="ccd-row">
          <!-- 源的下拉给该主机已存在的命名卷（快照里的事实），也允许手填宿主路径
               （allow-create）：两条合法形态协议都收，UI 不强迫二选一的入口。 -->
          <ElSelect
            v-model="row.source"
            class="ccd-row__input"
            filterable
            allow-create
            default-first-option
            placeholder="卷名或 /宿主/路径"
            :class="{ 'is-invalid': issues.mounts[i] !== '' }"
          >
            <ElOption v-for="v in volumeOptions" :key="v.value" :label="v.label" :value="v.value" />
          </ElSelect>
          <ElInput
            v-model="row.dest"
            class="ccd-row__input"
            placeholder="/容器/内/路径"
            :class="{ 'is-invalid': issues.mounts[i] !== '' }"
          />
          <ElCheckbox v-model="row.ro" class="ccd-row__ro" title="挂载为只读">只读</ElCheckbox>
          <ElButton
            size="small"
            text
            type="danger"
            title="删除这一行"
            @click="removeRow(form.mounts, i)"
          >
            删除
          </ElButton>
          <div v-if="issues.mounts[i]" class="ccd-row__issue">{{ issues.mounts[i] }}</div>
        </div>
        <div class="ccd-group__ops">
          <ElButton
            size="small"
            text
            :disabled="form.mounts.length >= MAX_CREATE_LIST_ITEMS"
            @click="addMountRow"
          >
            ＋ 添加挂载
          </ElButton>
          <span v-if="issues.mountsCount" class="ccd-field__issue">{{ issues.mountsCount }}</span>
        </div>
      </section>

      <!-- ══ 资源限额：可留空（= 不限额）══ -->
      <section class="ccd-group">
        <div class="ccd-group__title">资源限额</div>
        <div class="ccd-pair">
          <label class="ccd-field">
            <span class="ccd-field__label">CPU 上限（核）</span>
            <div class="ccd-field__control">
              <ElInputNumber
                v-model="form.cpuLimit"
                class="ccd-field__input"
                :min="0"
                :max="MAX_CPU_LIMIT"
                :step="0.5"
                placeholder="不限额"
                controls-position="right"
              />
              <span v-if="issues.cpuLimit" class="ccd-field__issue">{{ issues.cpuLimit }}</span>
              <span v-else class="ccd-field__hint"
                >留空或 0 = 不限额；上限 {{ MAX_CPU_LIMIT }} 核</span
              >
            </div>
          </label>
          <label class="ccd-field">
            <span class="ccd-field__label">内存上限（MB）</span>
            <div class="ccd-field__control">
              <ElInputNumber
                v-model="form.memLimitMb"
                class="ccd-field__input"
                :min="0"
                :max="MAX_MEM_LIMIT_MB"
                :step="64"
                placeholder="不限额"
                controls-position="right"
              />
              <span v-if="issues.memLimitMb" class="ccd-field__issue">{{ issues.memLimitMb }}</span>
              <span v-else class="ccd-field__hint">
                留空或 0 = 不限额；上限 {{ MAX_MEM_LIMIT_MB }} MB
              </span>
            </div>
          </label>
        </div>
      </section>

      <!-- ══ 网络：该主机的网络下拉 ══ -->
      <section class="ccd-group">
        <div class="ccd-group__title">网络</div>
        <div class="ccd-field">
          <span class="ccd-field__label">接入网络</span>
          <div class="ccd-field__control">
            <ElSelect
              v-model="form.network"
              class="ccd-field__input"
              filterable
              clearable
              placeholder="不指定（默认网桥）"
            >
              <ElOption
                v-for="n in networkOptions"
                :key="n.value"
                :label="n.label"
                :value="n.value"
              />
            </ElSelect>
            <span class="ccd-field__hint">不指定 = 该主机的默认网桥</span>
          </div>
        </div>
      </section>

      <!-- ══ 等效命令预览：只读等宽块，实时生成 ══ -->
      <section class="ccd-group ccd-preview">
        <div class="ccd-group__title">等效命令</div>
        <pre class="ccd-preview__cmd">{{ runPreview }}</pre>
        <div class="ccd-group__hint">
          随表单实时生成，未填的项不会出现；实际下发的是协议指令，等价于这条命令的效果
        </div>
      </section>
    </div>

    <template #footer>
      <div class="ccd-foot">
        <ElButton :disabled="busy" @click="visibleModel = false">取消</ElButton>
        <ElButton
          type="primary"
          :loading="busy"
          :disabled="!dockerOkHosts.length"
          @click="onSubmit"
        >
          创建…
        </ElButton>
      </div>
    </template>

    <!-- 标准档确认：目标卡 = 镜像名 → 容器名（弹窗里要被再次核对的两个事实）。
         组件按注册表推导形态与按钮；插槽补一句「创建后是否启动」。 -->
    <DockerActionConfirm
      v-model="confirmVisible"
      action="container:create"
      :target="confirmTargetText"
      :options="confirmOptions"
      target-kind="容器"
      :loading="busy"
      @confirm="onConfirmSubmit"
    >
      <div class="ccd-confirm__line">
        {{ form.start ? '创建后将立即启动容器' : '仅创建容器，创建后不启动' }}
      </div>
    </DockerActionConfirm>
  </ElDrawer>
</template>

<script setup lang="ts">
  /**
   * 容器创建抽屉（切片 4a 前端半边）：从本地镜像到运行容器的最后一步。
   *
   * ── 两个入口，一份表单 ─────────────────────────────────────────────
   *   - 容器页工具栏「创建容器…」：跨主机表的主机维度进下拉（预选当前筛选的主机）；
   *   - 镜像详情「用此镜像创建…」：预填镜像与主机（页面自己的主机上下文），
   *     主机仍可换 —— 换了之后镜像是否在新主机本地由快照重新核对。
   *
   * ── 校验的分工 ────────────────────────────────────────────────────
   * 协议白名单的逐字段规则（正则/上限/条数）收在 utils/container-create.ts 的纯函数里
   * （组件只决定「什么时候显示哪条结论」）：填过的字段即时显错；空着的必填项在点
   * 「创建」那一刻才显错（不打扰还没开始填的人）。有问题的表单不进确认弹窗、不发指令
   * —— 协议层还有第二道（agent 会再验一遍），这里挡的是「发出去必被拒」的形态错误。
   *
   * ── 提交链路 ──────────────────────────────────────────────────────
   * 标准档确认（action-confirm，注册表驱动）→ 指令通道（hostId = 所选主机，
   * useDockerCmds 的先例）→ 成功：关抽屉 + 双次重拉（composable 里的立即 + 落定）；
   * 失败：结论句弹给用户、抽屉留着（改名/换镜像后可直接重试）。
   */
  import { computed, ref, watch } from 'vue'
  import {
    ElButton,
    ElCheckbox,
    ElDrawer,
    ElInput,
    ElInputNumber,
    ElMessage,
    ElOption,
    ElSelect,
    ElSwitch
  } from 'element-plus'
  import { useAppBreakpoints } from '@/hooks/core/useAppBreakpoints'
  import DockerActionConfirm from './action-confirm.vue'
  import { fetchDockerState, type DockerHostItem, type DockerStateResp } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { hostLabel } from '../utils/host'
  import {
    MAX_CPU_LIMIT,
    MAX_CREATE_LIST_ITEMS,
    MAX_MEM_LIMIT_MB,
    emptyCreateForm,
    hasCreateIssue,
    validateCreateForm,
    buildCreateOptions,
    buildRunPreview,
    parseCreatePayload,
    type CreateContainerForm,
    type CreateEnvRow,
    type CreateMountRow,
    type CreatePortRow,
    type CreateSuccessInfo,
    type RestartPolicy
  } from '../utils/container-create'

  defineOptions({ name: 'DockerCreateContainerDrawer' })

  const props = withDefaults(
    defineProps<{
      /** 抽屉开关（v-model）。 */
      modelValue: boolean
      /** 可管主机清单（两个入口的页面都已持有：容器页来自统一表的 hosts，镜像页来自主机上下文）。 */
      hosts: DockerHostItem[]
      /** 打开时预选的主机 id（在清单且 Docker 可用时生效，否则落到第一台可用主机）。 */
      initialHostId?: string
      /** 打开时预填的镜像引用（镜像详情入口传页面正看的镜像）。 */
      initialImage?: string
      /** 创建成功后的重拉（useDockerCmds 的 refresh：立即一次 + 落定一次）。 */
      refresh?: () => void | Promise<void>
    }>(),
    { initialHostId: '', initialImage: '', refresh: undefined }
  )

  const emit = defineEmits<{
    'update:modelValue': [value: boolean]
    /** 创建成功（载荷里的完整 id / 短 id / 是否已启动 —— 页面据此可跳转或提示）。 */
    created: [info: CreateSuccessInfo]
  }>()

  const { smaller } = useAppBreakpoints()
  // 表单抽屉比日志/终端抽屉窄一档；平板竖屏以下全屏（列表编辑器的行要摆得下）。
  const drawerSize = computed(() => (smaller('tablet').value ? '100%' : '600px'))

  const visibleModel = computed({
    get: () => props.modelValue,
    set: (value: boolean) => emit('update:modelValue', value)
  })

  // ── 表单与主机 ───────────────────────────────────────────────────
  const form = ref<CreateContainerForm>(emptyCreateForm())
  /** 所选主机（指令按它派发；快照按它拉）。 */
  const formHostId = ref('')
  /** 点过一次「创建」后必填项的空值也开始显错（显错口径见文件头）。 */
  const submitAttempted = ref(false)

  /** Docker 可用的主机才进下拉：不可用的主机上创建必然失败，不如不给选项。 */
  const dockerOkHosts = computed(() => props.hosts.filter((h) => h.dockerOk === true))

  /** 重启策略的展示文案（值照协议枚举；'' = 缺席 = docker 默认 no，语义同 no）。 */
  const RESTART_POLICY_OPTIONS: { value: RestartPolicy; label: string }[] = [
    { value: 'on-failure', label: '失败时自动重启' },
    { value: 'always', label: '总是自动重启' },
    { value: 'unless-stopped', label: '除非手动停止，总是重启' }
  ]

  // ── 主机快照（镜像/卷/网络的选项来源；与镜像详情页同一拉取纪律）──
  const state = ref<DockerStateResp | null>(null)
  const stateLoading = ref(false)
  const stateError = ref(false)
  let stateSeq = 0

  async function loadState() {
    if (!formHostId.value) return
    const seq = ++stateSeq
    stateLoading.value = true
    stateError.value = false
    try {
      const res = await fetchDockerState(formHostId.value)
      if (seq !== stateSeq) return
      state.value = res
    } catch {
      if (seq !== stateSeq) return
      state.value = null
      stateError.value = true
    } finally {
      if (seq === stateSeq) stateLoading.value = false
    }
  }

  /** 打开抽屉：重置草稿（预选主机/预填镜像）并重拉快照 —— 上次关抽屉后用户可能刚拉了镜像。 */
  function openDrawer() {
    submitAttempted.value = false
    form.value = emptyCreateForm()
    const ok = dockerOkHosts.value
    const want = props.initialHostId
    formHostId.value = want && ok.some((h) => h.id === want) ? want : (ok[0]?.id ?? '')
    if (props.initialImage) form.value.image = props.initialImage
    void loadState()
  }

  watch(
    () => props.modelValue,
    (open) => {
      if (open) openDrawer()
    },
    // immediate：入口页面可能在抽屉已是打开态时才挂载它（v-model 初值即 true 的用例
    // 与组件测试都走这条路）；没有 immediate 时这次打开会被静默吞掉。
    { immediate: true }
  )

  /** 换主机：重拉快照（镜像清单/卷/网络都是主机事实）；表单保留 —— 换机不等于重填。 */
  function onHostChange() {
    void loadState()
  }

  // ── 选项（都从快照现算：换主机即换清单）──────────────────────────
  /** 镜像选项：仓库标签逐个入列；悬空镜像没有标签，按完整 id 引用（短 id 只是展示惯例）。 */
  const imageOptions = computed(() => {
    const seen = new Set<string>()
    const out: { value: string; label: string }[] = []
    for (const img of state.value?.images ?? []) {
      const tags = img.repoTags ?? []
      for (const t of tags) {
        if (!seen.has(t)) {
          seen.add(t)
          out.push({ value: t, label: t })
        }
      }
      if (!tags.length && !seen.has(img.id)) {
        seen.add(img.id)
        out.push({
          value: img.id,
          label: `${img.id.replace(/^sha256:/, '').slice(0, 12)}（无标签）`
        })
      }
    }
    return out
  })

  const imagePlaceholder = computed(() =>
    stateLoading.value ? '读取镜像清单中…' : '选择或输入镜像引用（仓库:标签）'
  )

  /** 挂载源选项：该主机的命名卷（手填宿主路径走 allow-create）。 */
  const volumeOptions = computed(() =>
    (state.value?.volumes ?? []).map((v) => ({ value: v.name, label: v.name }))
  )

  /**
   * 网络选项：快照里的网络去掉「接不进去/接了等于没接」的两种 ——
   *   - none：挂上它 = 无网络，与「不指定（默认网桥）」是两个相反的效果，
   *     放在下拉里只会被当成普通网络误选；
   *   - docker_gwbridge：swarm 的内部网桥，daemon 不接受手动接入。
   */
  const UNATTACHABLE_NETWORKS = new Set(['none', 'docker_gwbridge'])
  const networkOptions = computed(() =>
    (state.value?.networks ?? [])
      .filter((n) => !UNATTACHABLE_NETWORKS.has(n.name))
      .map((n) => ({ value: n.name, label: n.driver ? `${n.name}（${n.driver}）` : n.name }))
  )

  // ── 校验（规则在 utils；这里只做展示决策）────────────────────────
  /**
   * 镜像是否在该主机本地：快照未到/读取失败时返回 true —— 「还没读到清单」不是
   * 「主机上没有」，误报会把用户引去重复拉取；真缺失时 agent 的结论句会兜底。
   */
  function imageExistsOnHost(ref: string): boolean {
    const images = state.value?.images
    if (!images) return true
    return images.some((i) => (i.repoTags ?? []).includes(ref) || i.id === ref)
  }

  const issues = computed(() => validateCreateForm(form.value, imageExistsOnHost))
  const hasIssue = computed(() => hasCreateIssue(issues.value))
  const canSubmit = computed(() => !hasIssue.value && formHostId.value !== '')

  /** 镜像的显错口径：填过或点过创建才说（空着不打扰）。 */
  const imageIssueText = computed(() => {
    if (issues.value.image === '') return ''
    return form.value.image !== '' || submitAttempted.value ? issues.value.image : ''
  })
  const nameIssueText = computed(() => {
    if (issues.value.name === '') return ''
    return form.value.name !== '' || submitAttempted.value ? issues.value.name : ''
  })

  const runPreview = computed(() => buildRunPreview(form.value))

  // ── 列表编辑器（加减行；上限 32 与协议同源）────────────────────────
  function addPortRow() {
    if (form.value.ports.length >= MAX_CREATE_LIST_ITEMS) return
    form.value.ports.push({ host: '', container: '', proto: 'tcp' } satisfies CreatePortRow)
  }
  function addEnvRow() {
    if (form.value.env.length >= MAX_CREATE_LIST_ITEMS) return
    form.value.env.push({ key: '', value: '' } satisfies CreateEnvRow)
  }
  function addMountRow() {
    if (form.value.mounts.length >= MAX_CREATE_LIST_ITEMS) return
    form.value.mounts.push({ source: '', dest: '', ro: false } satisfies CreateMountRow)
  }
  function removeRow<T>(list: T[], index: number) {
    list.splice(index, 1)
  }

  // ── 提交：标准档确认 → 指令通道 ──────────────────────────────────
  const confirmVisible = ref(false)
  const confirmOptions = computed(() => buildCreateOptions(form.value))
  /** 目标卡：镜像名 → 容器名（没起名就只有镜像名）。 */
  const confirmTargetText = computed(() => {
    const image = form.value.image.trim()
    const name = form.value.name.trim()
    return name ? `${image} → ${name}` : image
  })

  function onSubmit() {
    submitAttempted.value = true
    // 有问题不弹确认（错误已在字段旁）；无主机也不弹（没地方创建）。
    if (!canSubmit.value) return
    confirmVisible.value = true
  }

  const { run, busy } = useDockerCmds({
    hostId: () => formHostId.value,
    refresh: () => props.refresh?.()
  })

  /** 确认弹窗提交：按所选主机派发 create；成功关抽屉（重拉在 composable 里双次执行）。 */
  async function onConfirmSubmit(payload: { confirm: string; force: boolean }) {
    const res = await run({
      action: 'container:create',
      options: confirmOptions.value,
      confirm: payload.confirm
    })
    confirmVisible.value = false
    if (res.ok) {
      // 成功附注优先用结果的 detail（agent 给的「已创建容器 <短 id>」）。
      ElMessage.success(res.detail || '容器已创建')
      emit('created', parseCreatePayload(res.payload))
      visibleModel.value = false
      return
    }
    // 失败不关抽屉：结论句已弹出（镜像缺失/同名冲突都在这里给），改完可直接重试。
    ElMessage.error(runErrorMessage(res, '创建未完成'))
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;

  // ── 头部 ───────────────────────────────────────────────
  .ccd-head {
    &__title {
      margin: 0;
      font-size: 16px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__sub {
      margin: 4px 0 0;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }

  // ── 表单骨架：分组 + 字段 ───────────────────────────────
  .ccd-form {
    display: flex;
    flex-direction: column;
    gap: 20px;
  }

  .ccd-group {
    &__title {
      margin-bottom: 8px;
      font-size: 13px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__hint {
      margin-bottom: 8px;
      font-size: 12px;
      line-height: 1.5;
      color: var(--el-text-color-secondary);
    }

    // 「＋添加」与区块级结论（条数超限）同一行：加行入口旁边就是那条约束。
    &__ops {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      align-items: center;
      margin-top: 4px;
    }
  }

  .ccd-field {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 10px;
    align-items: flex-start;
    margin-bottom: 12px;

    &__label {
      flex: 0 0 72px;
      padding-top: 5px;
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    &__control {
      flex: 1;
      min-width: 200px;
      display: flex;
      flex-direction: column;
      gap: 4px;
    }

    // 结论句三级：错误（红）引导修复，提示（灰）解释语义。
    &__issue {
      font-size: 12px;
      line-height: 1.5;
      color: var(--el-color-danger);
    }

    &__hint {
      font-size: 12px;
      line-height: 1.5;
      color: var(--el-text-color-secondary);
    }

    &__retry {
      color: var(--el-color-primary);
      cursor: pointer;
      margin-left: 4px;
    }
  }

  // 开关行：开关与解释并排（解释句随开关翻转，两种效果都说清楚）。
  .ccd-switch {
    flex-direction: row;
    align-items: center;
    gap: 10px;
  }

  // 资源限额两字段并排（都短）；窄屏由 ccd-field 的 flex-wrap 兜底换行。
  .ccd-pair {
    display: flex;
    flex-wrap: wrap;
    gap: 0 16px;

    > .ccd-field {
      flex: 1 1 220px;
    }
  }

  // ── 列表编辑器行：输入并排 + 删除；行内错误换行到整行下方 ──
  .ccd-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 8px;
    align-items: center;
    margin-bottom: 8px;

    &__input {
      flex: 1 1 120px;

      // 半行/越界的即时视觉反馈（结论句同时在行下方给出）。
      &.is-invalid :deep(.el-input__wrapper),
      &.is-invalid :deep(.el-select__wrapper) {
        box-shadow: 0 0 0 1px var(--el-color-danger) inset;
      }

      &--wide {
        flex: 2 1 160px;
      }
    }

    &__proto {
      flex: 0 0 88px;
      width: 88px;
    }

    &__ro {
      flex: 0 0 auto;
      margin-right: 2px;
      height: auto;
    }

    &__issue {
      flex: 1 1 100%;
      font-size: 12px;
      line-height: 1.5;
      color: var(--el-color-danger);
    }
  }

  // ── 命令预览：等宽只读块（长参数换行不断版）──────────────
  .ccd-preview__cmd {
    margin: 0 0 6px;
    padding: 10px 12px;
    border-radius: 6px;
    background: var(--el-fill-color-light);
    border: 1px solid var(--el-border-color-lighter);
    font-family: var(--el-font-family-mono, ui-monospace, 'SFMono-Regular', Consolas, monospace);
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-text-color-regular);
    white-space: pre-wrap;
    word-break: break-all;
    user-select: all;
  }

  // ── 底部按钮 ────────────────────────────────────────────
  .ccd-foot {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
  }

  // 确认弹窗的补充结论行（插槽内容）：与弹窗内既有结论句同一视觉档。
  .ccd-confirm__line {
    margin-top: 6px;
    font-size: 13px;
    line-height: 1.5;
    color: var(--el-text-color-secondary);
  }

  // 手机横屏（<768）：字段标签占满一行，行内输入不再挤成一条缝。
  @include respond-below('tablet') {
    .ccd-field__label {
      flex: 1 1 100%;
      padding-top: 0;
    }

    .ccd-row__input {
      flex: 1 1 100%;
    }

    .ccd-row__proto {
      flex: 1 1 88px;
    }
  }
</style>
