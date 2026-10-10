<template>
  <!-- 单根（single-root 守卫）：ElDialog 是唯一根，append-to-body 脱离表卡。 -->
  <ElDialog
    :model-value="modelValue"
    :title="hasTarget ? '修改目标版本' : '声明目标版本'"
    width="520px"
    :close-on-click-modal="false"
    append-to-body
    @update:model-value="emit('update:modelValue', $event)"
    @closed="reset"
  >
    <div class="itd">
      <!-- 要声明的对象：仓库:标签（等宽全文，长引用不折行由 CSS 处理）。 -->
      <div class="itd__ref">{{ repoTag }}</div>

      <!-- 候选摘要：多数派 + 少数派各一行，点选一个作为目标 —— 手输 hex 是错误高发面，
           首选「在现状分布里点一个」。主机数照行内口径给硬事实。 -->
      <ElRadioGroup v-model="selectedDigest" class="itd__cands" :disabled="loading">
        <ElRadio v-for="c in candidates" :key="c.digest" class="itd__cand" :value="c.digest">
          <span class="itd__digest" :title="c.digest">{{ shortDigest(c.digest) }}</span>
          <span class="itd__meta">{{ c.dominant ? '多数派 · ' : '' }}{{ c.hostCount }} 台</span>
        </ElRadio>
      </ElRadioGroup>

      <!-- 第二条路：直接粘贴 sha256:…（不合法就地给显式结论句）。 -->
      <div class="itd__manual">
        <label class="itd__label">或粘贴目标摘要</label>
        <ElInput v-model="manualDigest" placeholder="sha256:…" :disabled="loading" clearable />
        <p v-if="manualError" class="itd__error">{{ manualError }}</p>
      </div>

      <!-- 提交失败的就地结论句（服务端 msg 优先）——对话框开着，不放 toast。 -->
      <p v-if="submitError" class="itd__error">{{ submitError }}</p>
    </div>

    <template #footer>
      <ElButton :disabled="loading" @click="emit('update:modelValue', false)">取消</ElButton>
      <ElButton type="primary" :disabled="!canSubmit" :loading="loading" @click="submit">
        {{ hasTarget ? '修改目标' : '声明目标' }}
      </ElButton>
    </template>
  </ElDialog>
</template>

<script setup lang="ts">
  /**
   * 声明/修改镜像目标版本（Wave 4c，漂移页的旗舰入口）。
   *
   * 目标是「**期望的内容摘要**」—— 它只作**判定基准**：谁与它不同即待对齐，分发后也
   * 拿它复核收敛。它是比对基准，**不是**分发的拉取引用（分发按标签拉取，见漂移页）。
   *
   * 两条输入路都给（手输 hex 是错误高发面）：
   *   ① 在**现状分布里点一个**摘要（多数派 + 少数派，带各自主机数）—— 目标通常是
   *      某台已被认可的主机上的那枚摘要；
   *   ② 或直接粘贴 `sha256:…`（覆盖点选）。非空且不以 `sha256:` 开头时给显式结论句
   *      （服务端也是这么判的）。
   *
   * 提交走 POST /docker/image-targets（docker:manage，upsert）；成功后 emit saved
   * 让页面重拉本页（行上随即出现目标短显与已对齐计数）。
   */
  import { computed, ref, watch } from 'vue'
  import { ElButton, ElDialog, ElInput, ElRadio, ElRadioGroup } from 'element-plus'
  import { setImageTarget, type DockerImageDriftItem } from '../api'

  defineOptions({ name: 'DockerImageTargetDialog' })

  const props = defineProps<{
    modelValue: boolean
    /** 要声明目标的漂移行（含现状摘要分布与既有目标）。 */
    row: DockerImageDriftItem | null
  }>()

  const emit = defineEmits<{
    (e: 'update:modelValue', value: boolean): void
    (e: 'saved'): void
  }>()

  const selectedDigest = ref('')
  const manualDigest = ref('')
  const loading = ref(false)
  const submitError = ref('')

  const repoTag = computed(() => props.row?.repoTag ?? '')
  const hasTarget = computed(() => Boolean(props.row?.targetDigest))

  /** 一个候选摘要：现状分布里出现的一种 digest（多数派标出）。 */
  interface DigestCandidate {
    digest: string
    hostCount: number
    dominant: boolean
  }

  /** 候选 = 多数派 + 少数派（顺序即漂移行的多数派在前的读序）。 */
  const candidates = computed<DigestCandidate[]>(() => {
    const row = props.row
    if (!row) return []
    const out: DigestCandidate[] = []
    if (row.dominantDigest) {
      out.push({ digest: row.dominantDigest, hostCount: row.dominantHostCount, dominant: true })
    }
    for (const o of row.outliers ?? []) {
      out.push({ digest: o.digest, hostCount: o.hostCount, dominant: false })
    }
    return out
  })

  /** 摘要短显：剥 `sha256:` 取前 12 位（模块「长 id 短显」惯例）。 */
  function shortDigest(digest: string): string {
    const bare = (digest ?? '').replace(/^sha256:/, '')
    return bare ? bare.slice(0, 12) : '—'
  }

  /** 手输摘要的格式结论（与后端同一把尺：必须以 sha256: 开头）。 */
  const DIGEST_ERROR = '目标摘要必须以 sha256: 开头'
  const manualError = computed(() => {
    const v = manualDigest.value.trim()
    if (v === '') return ''
    return v.startsWith('sha256:') ? '' : DIGEST_ERROR
  })

  /** 生效摘要：粘贴优先（覆盖点选），否则取点选值。 */
  const effectiveDigest = computed(() => manualDigest.value.trim() || selectedDigest.value)

  const canSubmit = computed(
    () =>
      !loading.value && effectiveDigest.value !== '' && effectiveDigest.value.startsWith('sha256:')
  )

  function errMsg(e: unknown, fallback: string): string {
    const msg = (e as { message?: string })?.message
    return msg && msg.trim() !== '' ? msg : fallback
  }

  /** 打开时整表重置：预选当前目标（有则回填），清空手输与错误。 */
  watch(
    () => props.modelValue,
    (v) => {
      if (!v) return
      reset()
      selectedDigest.value = props.row?.targetDigest ?? ''
    },
    { immediate: true }
  )

  function reset() {
    selectedDigest.value = ''
    manualDigest.value = ''
    submitError.value = ''
    loading.value = false
  }

  async function submit() {
    if (!canSubmit.value) return
    loading.value = true
    submitError.value = ''
    try {
      await setImageTarget({ repoTag: repoTag.value, targetDigest: effectiveDigest.value })
      emit('saved')
      emit('update:modelValue', false)
    } catch (e) {
      submitError.value = errMsg(e, '声明失败，请稍后重试')
    } finally {
      loading.value = false
    }
  }
</script>

<style lang="scss" scoped>
  .itd {
    // 目标仓库引用：等宽，长引用允许折行（对话框窄，不强制单行）。
    &__ref {
      font-family: var(--art-font-family-mono);
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);
      word-break: break-all;
    }

    &__cands {
      display: flex;
      flex-direction: column;
      gap: 2px;
      margin-top: 12px;
    }

    &__cand {
      height: auto;
      margin-right: 0;
      padding: 4px 0;

      :deep(.el-radio__label) {
        display: inline-flex;
        gap: 8px;
        align-items: baseline;
      }
    }

    &__digest {
      font-family: var(--art-font-family-mono);
      color: var(--el-text-color-primary);
    }

    &__meta {
      font-size: 12px;
      color: var(--el-text-color-secondary);
    }

    &__manual {
      margin-top: 16px;
    }

    &__label {
      display: block;
      margin-bottom: 6px;
      font-size: 13px;
      color: var(--el-text-color-regular);
    }

    // 就地结论句（格式/提交失败）：与拉取对话框同一形态（红字、紧跟内容）。
    &__error {
      margin: 6px 0 0;
      font-size: 12px;
      line-height: 1.6;
      color: var(--aa-danger-text);
    }
  }
</style>
