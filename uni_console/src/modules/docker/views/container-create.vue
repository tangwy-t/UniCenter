<template>
  <!-- 单根（single-root 守卫在库：布局的 Transition 只支持单根，双根切页白屏）。 -->
  <div class="container-create art-full-height overflow-y-auto">
    <div class="container-create__inner p-4 pb-8 md:p-5">
      <!-- ============ 页头：返回 + 标题 + 一句「镜像须已在本地」 ============ -->
      <div class="cc-hero">
        <ArtButtonTable
          icon="ri:arrow-left-line"
          icon-class="bg-g-300/55 text-g-700"
          title="返回容器列表"
          @click="cancel"
        />
        <div class="min-w-0">
          <h2 class="cc-hero__title">创建容器</h2>
          <!-- 副标题只留硬约束（镜像须本地）—— 原「没有的话先到镜像页拉取」属
               去向说明句，按「零解释文案」纪律删除；镜像缺失的引导由镜像字段
               的即时校验（issues.image）就地给出。 -->
          <p class="cc-hero__sub">镜像须已在该主机本地</p>
        </div>
      </div>

      <!-- ============ 三态：主机清单未到 / 没有可管理 Docker 的主机 ============ -->
      <div v-if="ctx.loading && !ctx.hosts.length" class="cc-card cc-state">
        <ElSkeleton :rows="6" animated />
      </div>
      <div v-else-if="!dockerOkHosts.length" class="cc-card cc-state">
        <ElEmpty description="没有可管理 Docker 的主机">
          <ElButton size="small" @click="ctx.reload()">重新检测</ElButton>
        </ElEmpty>
      </div>

      <!-- ============ 表单体 ============
           六个分组自上而下按「必答 → 列表 → 高级」排布 —— 镜像与名称是创建的最小
           决定（协议必填只有 image），端口/变量/挂载是逐条加的清单，资源与网络是
           可留空的高级项。空组不折叠：看得见才有加第一行的入口。 -->
      <div v-else class="cc-card">
        <div class="cc-form">
          <!-- ══ 基本组：主机 / 镜像 / 名称 / 重启策略 / 创建后启动 ══ -->
          <section class="cc-group">
            <div class="cc-group__title">基本</div>

            <label class="cc-field">
              <span class="cc-field__label">主机</span>
              <div class="cc-field__control">
                <ElSelect
                  v-model="formHostId"
                  class="cc-field__input"
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
              </div>
            </label>

            <label class="cc-field">
              <span class="cc-field__label">镜像</span>
              <div class="cc-field__control">
                <!-- 可搜索 + 允许输入任意合法引用（digest/无标签镜像只能手填）；
                     选项来自该主机的本地镜像清单 —— create 不自动拉取，选了不在本地的
                     引用必失败，下面那条引导就是替 agent 提前说这句结论。 -->
                <ElSelect
                  v-model="form.image"
                  class="cc-field__input"
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
                <span v-if="imageIssueText" class="cc-field__issue">{{ imageIssueText }}</span>
                <span v-else-if="stateError" class="cc-field__issue">
                  读取该主机的镜像清单失败
                  <a class="cc-field__retry" @click.prevent="loadState">重试</a>
                </span>
                <span v-else-if="stateLoading" class="cc-field__hint"
                  >正在读取该主机的镜像清单…</span
                >
              </div>
            </label>

            <label class="cc-field">
              <span class="cc-field__label">容器名</span>
              <div class="cc-field__control">
                <ElInput
                  v-model="form.name"
                  class="cc-field__input"
                  placeholder="不填则由 Docker 自动命名"
                  clearable
                />
                <span v-if="nameIssueText" class="cc-field__issue">{{ nameIssueText }}</span>
              </div>
            </label>

            <div class="cc-field">
              <span class="cc-field__label">重启策略</span>
              <div class="cc-field__control">
                <ElSelect v-model="form.restartPolicy" class="cc-field__input">
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

            <div class="cc-field">
              <span class="cc-field__label">创建后启动</span>
              <div class="cc-field__control cc-switch">
                <ElSwitch v-model="form.start" />
                <span class="cc-field__hint">
                  {{ form.start ? '创建完成后立即启动容器' : '仅创建不启动，稍后可在容器页启动' }}
                </span>
              </div>
            </div>
          </section>

          <!-- ══ 端口：宿主:容器 + 协议，加减行 ══ -->
          <section class="cc-group">
            <div class="cc-group__title">端口映射</div>
            <div class="cc-group__hint">
              两端都须 1-65535（随机端口不支持：创建后无从知道去哪连）
            </div>
            <div v-for="(row, i) in form.ports" :key="`p${i}`" class="cc-row">
              <ElInput
                v-model="row.host"
                class="cc-row__input"
                placeholder="宿主端口"
                :class="{ 'is-invalid': issues.ports[i] !== '' }"
              />
              <ElInput
                v-model="row.container"
                class="cc-row__input"
                placeholder="容器端口"
                :class="{ 'is-invalid': issues.ports[i] !== '' }"
              />
              <ElSelect v-model="row.proto" class="cc-row__proto">
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
              <div v-if="issues.ports[i]" class="cc-row__issue">{{ issues.ports[i] }}</div>
            </div>
            <div class="cc-group__ops">
              <ElButton
                size="small"
                text
                :disabled="form.ports.length >= MAX_CREATE_LIST_ITEMS"
                @click="addPortRow"
              >
                ＋ 添加端口
              </ElButton>
              <span v-if="issues.portsCount" class="cc-field__issue">{{ issues.portsCount }}</span>
            </div>
          </section>

          <!-- ══ 环境变量：键/值两列，加减行 ══ -->
          <section class="cc-group">
            <div class="cc-group__title">环境变量</div>
            <div class="cc-group__hint">键须以字母或下划线开头；值可为空</div>
            <div v-for="(row, i) in form.env" :key="`e${i}`" class="cc-row">
              <ElInput
                v-model="row.key"
                class="cc-row__input"
                placeholder="变量名"
                :class="{ 'is-invalid': issues.env[i] !== '' }"
              />
              <ElInput
                v-model="row.value"
                class="cc-row__input cc-row__input--wide"
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
              <div v-if="issues.env[i]" class="cc-row__issue">{{ issues.env[i] }}</div>
            </div>
            <div class="cc-group__ops">
              <ElButton
                size="small"
                text
                :disabled="form.env.length >= MAX_CREATE_LIST_ITEMS"
                @click="addEnvRow"
              >
                ＋ 添加变量
              </ElButton>
              <span v-if="issues.envDuplicate" class="cc-field__issue">{{
                issues.envDuplicate
              }}</span>
              <span v-else-if="issues.envCount" class="cc-field__issue">{{ issues.envCount }}</span>
            </div>
          </section>

          <!-- ══ 挂载：源（命名卷/宿主路径）+ 目的地 + 只读，加减行 ══ -->
          <section class="cc-group">
            <div class="cc-group__title">挂载</div>
            <div class="cc-group__hint"
              >源可以是命名卷或以 / 开头的宿主路径；目的地是容器内绝对路径</div
            >
            <div v-for="(row, i) in form.mounts" :key="`m${i}`" class="cc-row">
              <!-- 源的下拉给该主机已存在的命名卷（快照里的事实），也允许手填宿主路径
                   （allow-create）：两条合法形态协议都收，UI 不强迫二选一的入口。 -->
              <ElSelect
                v-model="row.source"
                class="cc-row__input"
                filterable
                allow-create
                default-first-option
                placeholder="卷名或 /宿主/路径"
                :class="{ 'is-invalid': issues.mounts[i] !== '' }"
              >
                <ElOption
                  v-for="v in volumeOptions"
                  :key="v.value"
                  :label="v.label"
                  :value="v.value"
                />
              </ElSelect>
              <ElInput
                v-model="row.dest"
                class="cc-row__input"
                placeholder="/容器/内/路径"
                :class="{ 'is-invalid': issues.mounts[i] !== '' }"
              />
              <ElCheckbox v-model="row.ro" class="cc-row__ro" title="挂载为只读">只读</ElCheckbox>
              <ElButton
                size="small"
                text
                type="danger"
                title="删除这一行"
                @click="removeRow(form.mounts, i)"
              >
                删除
              </ElButton>
              <div v-if="issues.mounts[i]" class="cc-row__issue">{{ issues.mounts[i] }}</div>
            </div>
            <div class="cc-group__ops">
              <ElButton
                size="small"
                text
                :disabled="form.mounts.length >= MAX_CREATE_LIST_ITEMS"
                @click="addMountRow"
              >
                ＋ 添加挂载
              </ElButton>
              <span v-if="issues.mountsCount" class="cc-field__issue">{{
                issues.mountsCount
              }}</span>
            </div>
          </section>

          <!-- ══ 资源限额：可留空（= 不限额）══ -->
          <section class="cc-group">
            <div class="cc-group__title">资源限额</div>
            <div class="cc-pair">
              <label class="cc-field">
                <span class="cc-field__label">CPU 上限（核）</span>
                <div class="cc-field__control">
                  <!-- 不设 :max —— el-input-number 的 max 会把超限输入「静默钳制」到上限
                       （敲 64 回车，框里变 32，没有任何解释），与「填过的字段即时显错」的
                       校验范式相反。去掉 max：超限值原样保留，由 issues.cpuLimit 给出
                       可见结论，canSubmit 同步挡下（协议层还有第二道）。 -->
                  <ElInputNumber
                    v-model="form.cpuLimit"
                    class="cc-field__input"
                    :min="0"
                    :step="0.5"
                    placeholder="不限额"
                    controls-position="right"
                  />
                  <span v-if="issues.cpuLimit" class="cc-field__issue">{{ issues.cpuLimit }}</span>
                  <span v-else class="cc-field__hint"
                    >留空或 0 = 不限额；上限 {{ MAX_CPU_LIMIT }} 核</span
                  >
                </div>
              </label>
              <label class="cc-field">
                <span class="cc-field__label">内存上限（MB）</span>
                <div class="cc-field__control">
                  <!-- 与 CPU 同款问题同款修法：不设 :max —— el-input-number 的 max
                       会把超限输入「静默钳制」到上限（敲 40000 回车，框里变 32768，
                       没有任何解释），与「填过的字段即时显错」的校验范式相反。
                       去掉 max：超限值原样保留，由 issues.memLimitMb 给出可见结论，
                       canSubmit 同步挡下（协议层还有第二道）。 -->
                  <ElInputNumber
                    v-model="form.memLimitMb"
                    class="cc-field__input"
                    :min="0"
                    :step="64"
                    placeholder="不限额"
                    controls-position="right"
                  />
                  <span v-if="issues.memLimitMb" class="cc-field__issue">{{
                    issues.memLimitMb
                  }}</span>
                  <span v-else class="cc-field__hint">
                    留空或 0 = 不限额；上限 {{ MAX_MEM_LIMIT_MB }} MB
                  </span>
                </div>
              </label>
            </div>
          </section>

          <!-- ══ 网络：该主机的网络下拉 ══ -->
          <section class="cc-group">
            <div class="cc-group__title">网络</div>
            <div class="cc-field">
              <span class="cc-field__label">接入网络</span>
              <div class="cc-field__control">
                <ElSelect
                  v-model="form.network"
                  class="cc-field__input"
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
                <span class="cc-field__hint">不指定 = 该主机的默认网桥</span>
              </div>
            </div>
          </section>

          <!-- ══ 等效命令预览：只读等宽块，实时生成 ══ -->
          <section class="cc-group cc-preview">
            <div class="cc-group__title">等效命令</div>
            <pre class="cc-preview__cmd">{{ runPreview }}</pre>
            <div class="cc-group__hint">
              随表单实时生成，未填的项不会出现；实际下发的是协议指令，等价于这条命令的效果
            </div>
          </section>
        </div>

        <!-- 底部动作条：取消回列表（带上当前主机）；创建走标准档确认。 -->
        <div class="cc-foot">
          <ElButton :disabled="busy" @click="cancel">取消</ElButton>
          <ElButton type="primary" :loading="busy" @click="onSubmit">创建…</ElButton>
        </div>
      </div>
    </div>

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
      <div class="cc-confirm__line">
        {{ form.start ? '创建后将立即启动容器' : '仅创建容器，创建后不启动' }}
      </div>
    </DockerActionConfirm>
  </div>
</template>

<script setup lang="ts">
  /**
   * 容器创建页（8b 页面化，`/docker/containers/create`）：从本地镜像到运行容器的
   * 最后一步。被删的 create-container-drawer 表单**原样**平移到这里（校验规则、
   * 载荷形状、等效命令预览全在 utils/container-create.ts 的纯函数里，一行没动）；
   * 整页化换来的正是抽屉摆不下的东西：六组字段与逐条加减的清单在页面上是两列/
   * 整行的排布，窄屏也不会挤成一条缝（多分辨率适配）。
   *
   * ── 两个入口，一份表单 ─────────────────────────────────────────────
   *   - 容器页工具栏「创建容器…」：query 带当前筛选的主机（预选）；
   *   - 镜像详情「用此镜像创建…」：query 带主机与镜像（预填），主机仍可换 ——
   *     换了之后镜像是否在新主机本地由快照重新核对。
   * 预填走 **query**（抽屉时代是 props）：刷新/分享链接能把同一份初始现场还原，
   * 这是整页路由相对抽屉的直接收益。
   *
   * ── 校验的分工（原样）────────────────────────────────────────────
   * 协议白名单的逐字段规则（正则/上限/条数）收在 utils/container-create.ts 的纯函数里
   * （页面只决定「什么时候显示哪条结论」）：填过的字段即时显错；空着的必填项在点
   * 「创建」那一刻才显错（不打扰还没开始填的人）。有问题的表单不进确认弹窗、不发指令
   * —— 协议层还有第二道（agent 会再验一遍），这里挡的是「发出去必被拒」的形态错误。
   *
   * ── 提交链路（原样 + 整页化的收尾）───────────────────────────────
   * 标准档确认（action-confirm，注册表驱动）→ 指令通道（hostId = 所选主机）→
   * 成功：跳到**刚创建的容器详情页**（抽屉只能关掉自己，页面能把人送到下一步该看
   * 的地方；拿不到 id 时退回容器列表）→ 失败：结论句弹给用户、页面留着（改名/换镜像
   * 后可直接重试）。
   *
   * ── 权限 ────────────────────────────────────────────────────────
   * 路由 authMark 是 docker:manage（与「创建容器」入口按钮同档），MenuProcessor 的
   * 隐藏路由过滤已经把无权限用户挡在门外 —— 页面内不再重复判一次（那是死代码）。
   */
  import { computed, ref, watch } from 'vue'
  import { useRoute, useRouter } from 'vue-router'
  import {
    ElButton,
    ElCheckbox,
    ElEmpty,
    ElInput,
    ElInputNumber,
    ElMessage,
    ElOption,
    ElSelect,
    ElSkeleton,
    ElSwitch
  } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import DockerActionConfirm from '../components/action-confirm.vue'
  import { fetchDockerState, type DockerStateResp } from '../api'
  import { runErrorMessage, useDockerCmds } from '../composables/useDockerCmds'
  import { hostLabel } from '../utils/host'
  import { provideDockerHost } from '../utils/host-context'
  import {
    MAX_CPU_LIMIT,
    MAX_CREATE_LIST_ITEMS,
    MAX_MEM_LIMIT_MB,
    buildCreateOptions,
    buildRunPreview,
    emptyCreateForm,
    hasCreateIssue,
    parseCreatePayload,
    validateCreateForm,
    type CreateContainerForm,
    type CreateEnvRow,
    type CreateMountRow,
    type CreatePortRow,
    type RestartPolicy
  } from '../utils/container-create'

  defineOptions({ name: 'DockerContainerCreatePage' })

  const route = useRoute()
  const router = useRouter()
  // 主机清单是页面级上下文（provideDockerHost）：query host 是事实源，入口链接都带它。
  // reload 必须由页面自己发起（上下文只提供读取，不替页面决定拉取时机；先例见工作台
  // 经 useDockerHostState 的 `void ctx.reload()`）。
  const ctx = provideDockerHost()
  void ctx.reload()

  // ── 表单与主机 ───────────────────────────────────────────────────
  const form = ref<CreateContainerForm>(emptyCreateForm())
  /** 所选主机（指令按它派发；快照按它拉）。 */
  const formHostId = ref('')
  /** 点过一次「创建」后必填项的空值也开始显错（显错口径见文件头）。 */
  const submitAttempted = ref(false)

  /** Docker 可用的主机才进下拉：不可用的主机上创建必然失败，不如不给选项。 */
  const dockerOkHosts = computed(() => ctx.hosts.filter((h) => h.dockerOk === true))

  /** 重启策略的展示文案（值照协议枚举；'' = 缺席 = docker 默认 no，语义同 no）。 */
  const RESTART_POLICY_OPTIONS: { value: RestartPolicy; label: string }[] = [
    { value: 'on-failure', label: '失败时自动重启' },
    { value: 'always', label: '总是自动重启' },
    { value: 'unless-stopped', label: '除非手动停止，总是重启' }
  ]

  // ── 主机快照（镜像/卷/网络的选项来源）──
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

  /**
   * 进入页面：落定所选主机（query 里那台在且 Docker 可用就用它，否则第一台可用的）
   * 与预填镜像，然后拉该主机的快照。
   *
   * 预填主机不可用时落到首个可用主机、不猜一个创建必失败的地方 —— 与抽屉同一条口径。
   */
  function initFromQuery() {
    const ok = dockerOkHosts.value
    const want = String(route.query.host ?? '')
    formHostId.value = want && ok.some((h) => h.id === want) ? want : (ok[0]?.id ?? '')
    const image = String(route.query.image ?? '')
    if (image) form.value.image = image
    void loadState()
  }

  // 主机清单是异步到的（ctx.reload）：第一次到齐时落定初值 —— 只落一次，用户之后
  // 在表单里换的主机不被主机清单的后续刷新（例如刷新按钮）打回原样。
  let initialized = false
  watch(
    () => ctx.hosts.length > 0,
    (ready) => {
      if (!ready || initialized) return
      initialized = true
      initFromQuery()
    },
    { immediate: true }
  )

  /**
   * 换主机：重拉快照（镜像清单/卷/网络都是主机事实）；表单保留 —— 换机不等于重填。
   * query 同步写回：刷新/返回列表时主机不丢（host 是模块的主机作用域约定）。
   */
  function onHostChange() {
    void router.replace({ query: { ...route.query, host: formHostId.value } })
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

  const { run, busy } = useDockerCmds({ hostId: () => formHostId.value })

  /** 确认弹窗提交：按所选主机派发 create；成功把人送到刚创建的容器详情页。 */
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
      const created = parseCreatePayload(res.payload)
      if (created.id) {
        void router.push({
          path: `/docker/containers/${encodeURIComponent(created.id)}`,
          query: { host: formHostId.value }
        })
        return
      }
      // 载荷里没有 id（协议异常/裁剪）：至少把人带回列表，不留在表单上发愣。
      cancel()
      return
    }
    // 失败不离开表单：结论句已弹出（镜像缺失/同名冲突都在这里给），改完可直接重试。
    ElMessage.error(runErrorMessage(res, '创建未完成'))
  }

  /** 取消/失败收尾：回容器列表（带上当前主机，列表据此还原到同一台机器）。 */
  function cancel() {
    void router.push({ name: 'DockerContainers', query: { host: formHostId.value } })
  }
</script>

<style lang="scss" scoped>
  @use '@styles/core/breakpoints.scss' as *;
  @use './overview-tokens' as t;

  // 「取消 / 重新检测」等默认档按钮的主色文字对比度 AA：病灶与处方见 overview-tokens
  // 的 primary-text-aa（终审 QA D2·浅色实测 3.68:1）。
  @include t.primary-text-aa;

  /* 次要文字对比度 AA（P2 打磨批，与总览页同款处置）：EP 默认
     --el-text-color-secondary(#909399) 对白底只有 3.08:1（QA 实测 2.97–3.08），低于
     AA 正文线 → 页面范围内把它升到 regular 档（浅色 6.1:1、暗色随主题同样达标）。
     只重定义变量值，字段副提示/等效命令说明等一起达标，不碰元素样式与布局。 */
  .container-create {
    --el-text-color-secondary: var(--el-text-color-regular);
  }

  // ── 页头 ───────────────────────────────────────────────
  .cc-hero {
    display: flex;
    gap: 12px;
    align-items: center;
    margin-bottom: 12px;

    &__title {
      margin: 0;
      font-size: 18px;
      font-weight: 600;
      color: var(--el-text-color-primary);
    }

    &__sub {
      margin: 4px 0 0;
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }
  }

  // 表单卡片与三态占位同一外形（三态时不留一块与表单不同形的留白）。
  .cc-card {
    padding: 16px;
    background: var(--default-box-color);
    border-radius: 8px;
  }

  .cc-state {
    padding: 32px 16px;
  }

  // ── 表单骨架：分组 + 字段 ───────────────────────────────
  .cc-form {
    display: flex;
    flex-direction: column;
    gap: 20px;
  }

  .cc-group {
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

  .cc-field {
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
  .cc-switch {
    flex-direction: row;
    align-items: center;
    gap: 10px;
  }

  // 资源限额两字段并排（都短）；窄屏由 cc-field 的 flex-wrap 兜底换行。
  .cc-pair {
    display: flex;
    flex-wrap: wrap;
    gap: 0 16px;

    > .cc-field {
      flex: 1 1 220px;
    }
  }

  // ── 列表编辑器行：输入并排 + 删除；行内错误换行到整行下方 ──
  .cc-row {
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
  .cc-preview__cmd {
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

  // ── 底部按钮：与表单同一张卡片，右对齐（页面上不再有抽屉的固定底栏；
  //    留白靠内容区自身的滚动，长表单滚到底就能看见它）。 ──
  .cc-foot {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 20px;
    padding-top: 16px;
    border-top: 1px solid var(--el-border-color-lighter);
  }

  // 确认弹窗的补充结论行（插槽内容）：与弹窗内既有结论句同一视觉档。
  .cc-confirm__line {
    margin-top: 6px;
    font-size: 13px;
    line-height: 1.5;
    color: var(--el-text-color-secondary);
  }

  // 手机横屏（<768）：字段标签占满一行，行内输入不再挤成一条缝。
  @include respond-below('tablet') {
    .cc-field__label {
      flex: 1 1 100%;
      padding-top: 0;
    }

    .cc-row__input {
      flex: 1 1 100%;
    }

    .cc-row__proto {
      flex: 1 1 88px;
    }
  }
</style>
