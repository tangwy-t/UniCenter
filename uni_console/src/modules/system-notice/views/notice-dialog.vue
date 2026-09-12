<template>
  <el-dialog
    v-model="visible"
    :title="form.id ? '编辑通知公告' : '新增通知公告'"
    width="780px"
    top="5vh"
    append-to-body
  >
    <el-form :model="form" label-width="92px">
      <el-row :gutter="12">
        <el-col :span="12">
          <el-form-item label="公告标题" required>
            <el-input
              v-model="form.title"
              placeholder="请输入公告标题"
              maxlength="128"
              show-word-limit
            />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="公告类型" required>
            <el-select v-model="form.noticeType" placeholder="请选择类型" style="width: 100%">
              <el-option
                v-for="opt in noticeTypeOptions"
                :key="opt.value"
                :label="opt.label"
                :value="opt.value"
              />
            </el-select>
          </el-form-item>
        </el-col>

        <el-col :span="12">
          <el-form-item label="状态">
            <div class="status-readonly">
              <el-tag :type="statusDict.tagType(form.status ?? 0)" effect="light">
                {{ statusDict.labelOf(form.status ?? 0) }}
              </el-tag>
              <span class="status-hint">发布/撤回请在列表操作</span>
            </div>
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="优先级">
            <el-select v-model="form.priority" placeholder="请选择优先级" style="width: 100%">
              <el-option
                v-for="opt in priorityOptions"
                :key="opt.value"
                :label="opt.label"
                :value="opt.value"
              />
            </el-select>
          </el-form-item>
        </el-col>

        <el-col :span="24">
          <el-form-item label="接收范围" required>
            <el-radio-group v-model="form.targetType" @change="onTargetTypeChange">
              <el-radio v-for="opt in targetTypeOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </el-radio>
            </el-radio-group>
          </el-form-item>
        </el-col>

        <el-col :span="24">
          <el-form-item v-if="form.targetType === 1" label="选择角色" required>
            <el-select
              v-model="targetRoleIds"
              multiple
              filterable
              collapse-tags
              style="width: 100%"
              placeholder="请选择接收角色"
            >
              <el-option
                v-for="r in roleOptions"
                :key="r.value"
                :label="r.label"
                :value="r.value"
              />
            </el-select>
          </el-form-item>

          <el-form-item v-if="form.targetType === 2" label="选择部门" required>
            <el-tree-select
              v-model="targetDeptIds"
              :data="deptTree"
              :props="{ label: 'label', children: 'children' }"
              node-key="value"
              multiple
              check-strictly
              show-checkbox
              filterable
              clearable
              style="width: 100%"
              placeholder="请选择接收部门"
            />
          </el-form-item>

          <el-form-item v-if="form.targetType === 3" label="选择个人" required>
            <el-select
              v-model="targetUserIds"
              multiple
              filterable
              remote
              :remote-method="searchUsers"
              :loading="userLoading"
              collapse-tags
              style="width: 100%"
              placeholder="输入姓名/账号搜索并选择接收人"
            >
              <el-option
                v-for="u in userOptions"
                :key="u.value"
                :label="u.label"
                :value="u.value"
              />
            </el-select>
          </el-form-item>
        </el-col>

        <el-col :span="24">
          <el-form-item label="公告内容" required>
            <ArtWangEditor
              v-model="form.content"
              height="300px"
              placeholder="请输入公告内容..."
              :exclude-keys="editorExcludeKeys"
            />
          </el-form-item>
        </el-col>
      </el-row>
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
  import { ElMessage } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtWangEditor from '@/components/core/forms/art-wang-editor/index.vue'
  import { useDict } from '@/hooks/core/useDict'
  import { fetchUsers, fetchDepts, fetchAllRoles } from '@/modules/system-user/api'
  import { createNotice, updateNotice, fetchNoticeTargetUsers } from '../api'

  interface SelectOption {
    value: string | number
    label: string
  }

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureNoticeType, options: noticeTypeOptions } = useDict('sys_notice_type', {
    numeric: true
  })
  const { ensure: ensurePriority, options: priorityOptions } = useDict('sys_notice_priority', {
    numeric: true
  })
  const statusDict = useDict('sys_notice_status', { numeric: true })
  const { ensure: ensureTargetType, options: targetTypeOptions } = useDict(
    'sys_notice_publish_type',
    {
      numeric: true
    }
  )

  // 富文本工具栏精简:无图片/视频上传接口,隐藏对应分组
  const editorExcludeKeys = [
    'fontFamily',
    'group-image',
    'group-video',
    'insertVideo',
    'codeBlock',
    'todo'
  ]

  const visible = ref(false)
  const roleOptions = ref<SelectOption[]>([])
  const deptTree = ref<any[]>([])
  const userOptions = ref<SelectOption[]>([])
  const userLoading = ref(false)

  const targetRoleIds = ref<string[]>([])
  const targetDeptIds = ref<string[]>([])
  const targetUserIds = ref<string[]>([])

  const emptyForm = (): Api.Notice.Form => ({
    id: undefined,
    title: '',
    content: '',
    noticeType: undefined,
    priority: undefined,
    targetType: 0,
    targetIds: ''
  })
  const form = reactive<Api.Notice.Form>(emptyForm())

  // 状态 tag 由 sys_notice_status 字典渲染(list_class 驱动:0 草稿 info / 1 已发布 success / 2 已撤回 warning)

  async function loadSupportOptions() {
    const [roles, depts] = await Promise.all([fetchAllRoles(), fetchDepts()])
    // 防御性兜底：后端空数据时理论上应返回 []，但若异常返回 null，
    // 这里归一化为空数组，避免后续 .map 对 null 崩溃。
    roleOptions.value = (roles ?? []).map((r) => ({ value: r.id, label: r.name }))
    deptTree.value = buildDeptTree(depts ?? [])
  }

  function buildDeptTree(depts: Api.System.Dept[]): any[] {
    return depts.map((d) => ({
      value: d.id,
      label: d.name,
      children: d.children?.length ? buildDeptTree(d.children) : undefined
    }))
  }

  async function searchUsers(keyword: string) {
    const kw = keyword?.trim() || undefined
    userLoading.value = true
    try {
      const [byName, byReal] = await Promise.all([
        fetchUsers({ page: 1, pageSize: 50, ...(kw ? { username: kw } : {}) }),
        fetchUsers({ page: 1, pageSize: 50, ...(kw ? { realName: kw } : {}) })
      ])
      const seen = new Set(userOptions.value.map((o) => String(o.value)))
      const merged: SelectOption[] = [...userOptions.value]
      const push = (u: Api.System.User) => {
        if (seen.has(u.id)) return
        seen.add(u.id)
        merged.push({
          value: u.id,
          label: u.realName ? `${u.realName}（${u.username}）` : u.username
        })
      }
      ;(byName?.list ?? []).forEach(push)
      ;(byReal?.list ?? []).forEach(push)
      userOptions.value = merged
    } finally {
      userLoading.value = false
    }
  }

  function onTargetTypeChange() {
    targetRoleIds.value = []
    targetDeptIds.value = []
    targetUserIds.value = []
    form.targetIds = ''
  }

  async function open(row?: Api.Notice.Notice) {
    await Promise.all([
      ensureNoticeType(),
      ensurePriority(),
      statusDict.ensure(),
      ensureTargetType()
    ])
    await loadSupportOptions()
    Object.assign(form, emptyForm(), {
      id: row?.id,
      title: row?.title ?? '',
      content: row?.content ?? '',
      noticeType: row?.noticeType,
      priority: row?.priority,
      status: row?.status,
      targetType: row?.targetType ?? 0,
      targetIds: ''
    })
    targetRoleIds.value = []
    targetDeptIds.value = []
    targetUserIds.value = []
    await syncTargetArrays(form.targetType as number, row?.targetIds ?? '')
    visible.value = true
  }

  // 按类型把 targetIds 反解到各选择数组
  async function syncTargetArrays(type: number, ids: string) {
    const arr = (ids || '').split(',').filter((s) => s !== '')
    if (type === 1) targetRoleIds.value = arr
    else if (type === 2) targetDeptIds.value = arr
    else if (type === 3) {
      targetUserIds.value = arr
      if (arr.length) {
        const rows = await fetchNoticeTargetUsers(arr.join(','))
        userOptions.value = (rows ?? []).map((u) => ({
          value: u.id,
          label: u.realName ? `${u.realName}（${u.username}）` : u.username
        }))
      } else {
        userOptions.value = []
      }
    }
  }

  async function onSave() {
    if (!form.title?.trim()) {
      ElMessage.warning('请填写公告标题')
      return
    }
    if (form.noticeType === undefined || form.noticeType === null) {
      ElMessage.warning('请选择公告类型')
      return
    }
    const type = (form.targetType ?? 0) as number
    let ids = ''
    if (type === 1) ids = targetRoleIds.value.join(',')
    else if (type === 2) ids = targetDeptIds.value.join(',')
    else if (type === 3) ids = targetUserIds.value.join(',')
    if (type !== 0 && !ids) {
      ElMessage.warning('请选择接收对象（角色/部门/个人）')
      return
    }
    form.targetType = type
    form.targetIds = ids

    if (form.id) {
      await updateNotice(form.id, form)
    } else {
      await createNotice(form)
    }
    ElMessage.success('已保存')
    visible.value = false
    emit('saved')
  }

  defineExpose({ open })
</script>

<style lang="scss" scoped>
  .status-readonly {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .status-hint {
    font-size: 12px;
    color: var(--el-text-color-placeholder);
  }

  :deep(.el-form-item) {
    margin-bottom: 20px;
  }
</style>
