<template>
  <el-dialog
    v-model="visible"
    :title="form.id ? '编辑任务' : '新增任务'"
    width="640px"
    destroy-on-close
    :close-on-click-modal="false"
  >
    <el-form
      ref="formRef"
      :model="form"
      :rules="rules"
      label-width="100px"
      label-position="right"
      class="job-form"
    >
      <!-- ============ 基本信息 ============ -->
      <div class="form-section">
        <div class="form-section-title">
          <ArtSvgIcon icon="ri:information-line" />
          基本信息
        </div>
        <el-form-item v-if="!form.id" label="任务名称" prop="name">
          <el-input
            v-model="form.name"
            placeholder="请输入任务名称"
            maxlength="64"
            show-word-limit
          />
        </el-form-item>
        <el-form-item v-if="!form.id" label="任务组" prop="jobGroup">
          <el-input v-model="form.jobGroup" placeholder="如 default" maxlength="64" />
        </el-form-item>
        <el-form-item label="状态" prop="status">
          <el-radio-group v-model="form.status">
            <el-radio v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="启动补跑">
          <div class="startup-row">
            <el-switch v-model="form.runAtStartup" :active-value="1" :inactive-value="0" />
            <span class="field-hint">调度服务启动时立即执行一次（错过周期不追溯补跑）</span>
          </div>
        </el-form-item>
      </div>

      <!-- ============ 调度设置 ============ -->
      <div class="form-section">
        <div class="form-section-title">
          <ArtSvgIcon icon="ri:calendar-2-line" />
          调度设置
        </div>
        <el-form-item label="执行周期" prop="cronExpression">
          <JobCronField v-model="form.cronExpression" />
        </el-form-item>
        <el-form-item label="调用目标" prop="invokeTarget">
          <JobTargetParams
            ref="targetParamsRef"
            :target="form.invokeTarget ?? ''"
            :params="form.invokeParams ?? ''"
            @update:target="form.invokeTarget = $event"
            @update:params="form.invokeParams = $event"
          />
        </el-form-item>
      </div>

      <!-- ============ 执行策略 ============ -->
      <div class="form-section">
        <div class="form-section-title">
          <ArtSvgIcon icon="ri:settings-3-line" />
          执行策略
        </div>
        <el-form-item label="是否并发">
          <div class="policy-row">
            <el-radio-group v-model="form.concurrent">
              <el-radio v-for="opt in concurrentOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </el-radio>
            </el-radio-group>
            <span class="field-hint">禁止并发时，上次未执行完则跳过本次触发</span>
          </div>
        </el-form-item>
        <el-form-item label="失败重试">
          <div class="policy-row">
            <el-input-number
              v-model="form.retryCount"
              :min="0"
              :max="10"
              controls-position="right"
              aria-label="重试次数"
            />
            <span class="field-hint">次，每次间隔</span>
            <el-input-number
              v-model="form.retryInterval"
              :min="0"
              :max="3600"
              :disabled="!form.retryCount"
              controls-position="right"
              aria-label="重试间隔秒数"
            />
            <span class="field-hint">秒</span>
          </div>
        </el-form-item>
      </div>

      <!-- ============ 备注 ============ -->
      <div class="form-section">
        <div class="form-section-title">
          <ArtSvgIcon icon="ri:file-text-line" />
          备注
        </div>
        <el-form-item label="任务备注" prop="remark">
          <el-input
            v-model="form.remark"
            type="textarea"
            :rows="3"
            maxlength="512"
            show-word-limit
            placeholder="可选，记录任务用途、负责人等"
          />
        </el-form-item>
      </div>
    </el-form>

    <template #footer>
      <div class="art-dialog-footer">
        <ArtButtonTable
          icon="ri:close-line"
          iconClass="bg-g-300/55 text-g-700"
          title="取消"
          @click="visible = false"
        />
        <ArtButtonTable
          icon="ri:check-line"
          iconClass="bg-theme/12 text-theme"
          title="保存"
          @click="onSave"
        />
      </div>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
  import { reactive, ref } from 'vue'
  import type { FormInstance } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import JobCronField from '../components/job-cron-field.vue'
  import JobTargetParams from '../components/job-target-params.vue'
  import { ElMessage } from 'element-plus'
  import { useDict } from '@/hooks/core/useDict'
  import { useJobTargets } from '../composables/useJobTargets'
  import { evaluateCron } from '../utils/cron'
  import { createJob, updateJob } from '../api'

  defineOptions({ name: 'JobDialog' })

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDict('sys_job_status', {
    numeric: true
  })
  const { ensure: ensureConcurrent, options: concurrentOptions } = useDict('sys_job_concurrent', {
    numeric: true
  })
  const { ensure: ensureTargets } = useJobTargets()

  const visible = ref(false)
  const saving = ref(false)
  const formRef = ref<FormInstance>()
  const targetParamsRef = ref<InstanceType<typeof JobTargetParams>>()

  /** 表单模型：open() 时总是填充完整字段，类型上收敛 instrument 必填项 */
  interface JobFormModel extends Api.Job.Form {
    cronExpression: string
    invokeTarget: string
    invokeParams: string
  }

  const form = reactive<JobFormModel>({
    id: undefined,
    name: '',
    jobGroup: 'default',
    cronExpression: '',
    invokeTarget: '',
    invokeParams: '',
    concurrent: 1,
    retryCount: 0,
    retryInterval: 0,
    status: 1,
    runAtStartup: 0,
    remark: ''
  })

  /** 表单校验：cron 与参数合法性走组件本地校验，权威校验仍在服务端 */
  const rules = {
    name: [
      { required: true, message: '请输入任务名称', trigger: 'blur' },
      { max: 64, message: '长度不能超过 64 个字符', trigger: 'blur' }
    ],
    jobGroup: [
      { required: true, message: '请输入任务组', trigger: 'blur' },
      { max: 64, message: '长度不能超过 64 个字符', trigger: 'blur' }
    ],
    cronExpression: [
      { required: true, message: '请填写执行周期', trigger: ['blur', 'change'] },
      {
        validator: (_rule: unknown, value: string, callback: (e?: Error) => void) => {
          const res = evaluateCron(value)
          if (res.valid) callback()
          else callback(new Error(res.message))
        },
        trigger: ['blur', 'change']
      }
    ],
    invokeTarget: [{ required: true, message: '请选择调用目标', trigger: 'change' }],
    invokeParams: [
      {
        validator: (_rule: unknown, _value: string, callback: (e?: Error) => void) => {
          if (!targetParamsRef.value) {
            callback()
            return
          }
          const res = targetParamsRef.value.validate()
          if (res.valid) callback()
          else callback(new Error(res.message))
        },
        trigger: ['change', 'blur']
      }
    ]
  }

  async function open(row?: Api.Job.Job) {
    await Promise.all([ensureStatus(), ensureConcurrent(), ensureTargets()])
    Object.assign(
      form,
      row
        ? {
            id: row.id,
            name: row.name,
            jobGroup: row.jobGroup,
            cronExpression: row.cronExpression,
            invokeTarget: row.invokeTarget,
            invokeParams: row.invokeParams ?? '',
            concurrent: row.concurrent,
            retryCount: row.retryCount,
            retryInterval: row.retryInterval,
            status: row.status,
            runAtStartup: row.runAtStartup ?? 0,
            remark: row.remark ?? ''
          }
        : {
            id: undefined,
            name: '',
            jobGroup: 'default',
            cronExpression: '',
            invokeTarget: '',
            invokeParams: '',
            concurrent: 1,
            retryCount: 0,
            retryInterval: 0,
            status: 1,
            runAtStartup: 0,
            remark: ''
          }
    )
    formRef.value?.clearValidate()
    visible.value = true
  }

  async function onSave() {
    if (saving.value) return
    try {
      await formRef.value?.validate()
    } catch {
      return
    }
    saving.value = true
    try {
      if (form.id) {
        await updateJob(form.id, form)
      } else {
        await createJob(form)
      }
      ElMessage.success('已保存')
      visible.value = false
      emit('saved')
    } finally {
      saving.value = false
    }
  }

  defineExpose({ open })
</script>

<style scoped>
  .job-form {
    max-height: 60vh;
    overflow-y: auto;
  }

  .form-section {
    margin-bottom: 16px;
  }

  .form-section-title {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-bottom: 12px;
    font-size: 13px;
    font-weight: 600;
    color: var(--color-g-600, #7987a1);

    &::after {
      content: '';
      flex: 1;
      height: 1px;
      margin-left: 8px;
      background: var(--default-border, #e2e8ee);
    }
  }

  .startup-row,
  .policy-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }

  .field-hint {
    font-size: 12px;
    color: var(--color-g-500, #949eb7);
  }
</style>
