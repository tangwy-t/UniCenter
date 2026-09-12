<template>
  <el-dialog
    v-model="visible"
    :title="form.id ? '编辑角色' : '新增角色'"
    width="640px"
    append-to-body
    :close-on-click-modal="false"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="92px">
      <el-row :gutter="12">
        <el-col :span="12">
          <el-form-item label="角色名称" prop="name">
            <el-input
              v-model="form.name"
              placeholder="请输入角色名称"
              maxlength="64"
              show-word-limit
            />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="角色编码" prop="code">
            <div class="code-input-wrap">
              <el-input
                v-model="form.code"
                placeholder="如 admin"
                :disabled="!!form.id"
                maxlength="64"
              />
              <ElTooltip
                placement="top"
                content="控制器中定义的权限字符，用于后端鉴权判断，如 admin（超级管理员）、common（普通角色）"
              >
                <ArtSvgIcon icon="ri:question-line" class="tip-icon" />
              </ElTooltip>
            </div>
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="数据权限" prop="dataScope">
            <el-select v-model="form.dataScope" style="width: 100%">
              <el-option
                v-for="o in dataScopeOptions"
                :key="o.value"
                :label="o.label"
                :value="o.value"
              />
            </el-select>
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="显示顺序" prop="sort">
            <el-input-number
              v-model="form.sort"
              :min="0"
              :max="9999"
              controls-position="right"
              style="width: 160px"
            />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="状态">
            <el-radio-group v-model="form.status">
              <el-radio v-for="opt in statusOptions" :key="opt.value" :value="Number(opt.value)">
                {{ opt.label }}
              </el-radio>
            </el-radio-group>
          </el-form-item>
        </el-col>
      </el-row>

      <el-form-item label="备注" prop="remark">
        <el-input
          v-model="form.remark"
          type="textarea"
          :rows="2"
          placeholder="请输入备注"
          maxlength="512"
          show-word-limit
        />
      </el-form-item>

      <el-form-item label="菜单权限">
        <div class="tree-panel">
          <div class="tree-toolbar">
            <el-checkbox v-model="menuExpand" @change="onMenuExpand">展开/折叠</el-checkbox>
            <el-checkbox v-model="menuNodeAll" @change="onMenuNodeAll">全选/全不选</el-checkbox>
            <el-checkbox v-model="menuCheckStrictly">父子联动</el-checkbox>
          </div>
          <el-tree
            ref="menuTreeRef"
            class="tree-body"
            :data="menuTree"
            :props="{ label: 'name', children: 'children' }"
            node-key="id"
            show-checkbox
            :check-strictly="!menuCheckStrictly"
            :expand-on-click-node="false"
            empty-text="加载中，请稍候"
          />
        </div>
      </el-form-item>

      <el-form-item v-if="form.dataScope === 2" label="数据部门">
        <div class="tree-panel">
          <div class="tree-toolbar">
            <el-checkbox v-model="deptExpand" @change="onDeptExpand">展开/折叠</el-checkbox>
            <el-checkbox v-model="deptNodeAll" @change="onDeptNodeAll">全选/全不选</el-checkbox>
            <el-checkbox v-model="deptCheckStrictly">父子联动</el-checkbox>
          </div>
          <el-tree
            ref="deptTreeRef"
            class="tree-body"
            :data="deptTree"
            :props="{ label: 'name', children: 'children' }"
            node-key="id"
            show-checkbox
            :check-strictly="!deptCheckStrictly"
            :expand-on-click-node="false"
            empty-text="加载中，请稍候"
          />
        </div>
      </el-form-item>
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
  import { nextTick, reactive, ref } from 'vue'
  import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'
  import { useDict } from '@/hooks/core/useDict'
  import { createRole, updateRole, fetchRole } from '../api'
  import { fetchMenus } from '@/modules/system-menu/api'
  import { fetchDepts } from '@/modules/system-dept/api'

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDict('sys_role_status', {
    numeric: true
  })

  const dataScopeOptions = [
    { label: '全部数据权限', value: 1 },
    { label: '自定数据权限', value: 2 },
    { label: '本部门数据权限', value: 3 },
    { label: '本部门及以下数据权限', value: 4 },
    { label: '仅本人数据权限', value: 5 }
  ]

  const visible = ref(false)
  const saving = ref(false)
  const formRef = ref<FormInstance>()
  const menuTreeRef = ref()
  const deptTreeRef = ref()
  const menuTree = ref<Api.System.Menu[]>([])
  const deptTree = ref<Api.System.Dept[]>([])

  // 树工具栏状态(RuoYi 语义):均默认「父子联动」开启
  const menuExpand = ref(false)
  const menuNodeAll = ref(false)
  const menuCheckStrictly = ref(true)
  const deptExpand = ref(true)
  const deptNodeAll = ref(false)
  const deptCheckStrictly = ref(true)

  const emptyForm = () => ({
    id: undefined as string | undefined,
    name: '',
    code: '',
    dataScope: 1,
    sort: 0,
    status: 1,
    remark: '',
    menuIds: [] as string[],
    deptIds: [] as string[]
  })
  const form = reactive(emptyForm())

  const rules: FormRules = {
    name: [{ required: true, message: '请输入角色名称', trigger: 'blur' }],
    code: [{ required: true, message: '请输入角色编码', trigger: 'blur' }],
    dataScope: [{ required: true, message: '请选择数据权限', trigger: 'change' }],
    sort: [{ required: true, message: '请输入显示顺序', trigger: 'blur' }]
  }

  function resetTreeState() {
    menuExpand.value = false
    menuNodeAll.value = false
    menuCheckStrictly.value = true
    deptExpand.value = true
    deptNodeAll.value = false
    deptCheckStrictly.value = true
  }

  async function open(row?: Api.System.Role) {
    await ensureStatus()
    const [menus, depts] = await Promise.all([fetchMenus(), fetchDepts()])
    menuTree.value = menus
    deptTree.value = depts

    Object.assign(form, emptyForm())

    // 编辑时以服务端详情为准(列表接口不带 menuIds/deptIds,直接用行数据会清空权限)
    if (row?.id) {
      const detail = await fetchRole(row.id)
      Object.assign(form, {
        id: detail.id,
        name: detail.name,
        code: detail.code,
        dataScope: Number(detail.dataScope ?? 1),
        sort: Number(detail.sort ?? 0),
        status: Number(detail.status ?? 1),
        remark: detail.remark ?? '',
        menuIds: detail.menuIds ?? [],
        deptIds: detail.deptIds ?? []
      })
    }

    resetTreeState()
    visible.value = true
    await nextTick()
    formRef.value?.clearValidate()

    // 逐 key 勾选(deep=false),父子联动模式下父节点按子节点状态自动半选
    menuTreeRef.value?.setCheckedKeys([])
    deptTreeRef.value?.setCheckedKeys([])
    for (const key of form.menuIds) {
      await nextTick()
      menuTreeRef.value?.setChecked(key, true, false)
    }
    for (const key of form.deptIds) {
      await nextTick()
      deptTreeRef.value?.setChecked(key, true, false)
    }
  }

  /* ── 树工具栏 ───────────────────────────────────── */
  function setAllTreeExpanded(treeRef: any, expanded: boolean) {
    const store = treeRef?.value?.store
    const nodes: any[] = store?._getAllNodes?.() ?? Object.values(store?.nodesMap ?? {})
    nodes.forEach((node: any) => {
      if (node) node.expanded = expanded
    })
  }
  function setAllTreeChecked(treeRef: any, treeData: any[], checked: boolean) {
    const keys: any[] = []
    const walk = (nodes: any[] | undefined) => {
      if (!nodes) return
      nodes.forEach((n) => {
        keys.push(n.id)
        if (n.children?.length) walk(n.children)
      })
    }
    walk(treeData)
    treeRef?.value?.setCheckedKeys(checked ? keys : [])
  }

  function onMenuExpand(v: boolean | string | number) {
    setAllTreeExpanded(menuTreeRef, Boolean(v))
  }
  function onMenuNodeAll(v: boolean | string | number) {
    setAllTreeChecked(menuTreeRef, menuTree.value, Boolean(v))
  }
  function onDeptExpand(v: boolean | string | number) {
    setAllTreeExpanded(deptTreeRef, Boolean(v))
  }
  function onDeptNodeAll(v: boolean | string | number) {
    setAllTreeChecked(deptTreeRef, deptTree.value, Boolean(v))
  }

  /* ── 保存 ───────────────────────────────────────── */
  // RuoYi 语义:已勾选 + 半选,目录/菜单/按钮全量回传
  function collectCheckedKeys(treeRef: any): string[] {
    const tree = treeRef?.value
    if (!tree) return []
    const checked = (tree.getCheckedKeys() ?? []) as any[]
    const half = (tree.getHalfCheckedKeys() ?? []) as any[]
    return Array.from(new Set([...checked, ...half])).map(String)
  }

  async function onSave() {
    const valid = await formRef.value?.validate().catch(() => false)
    if (!valid) return

    const payload: Api.System.RoleForm = {
      ...form,
      dataScope: Number(form.dataScope ?? 1),
      sort: Number(form.sort ?? 0),
      status: Number(form.status ?? 1),
      menuIds: collectCheckedKeys(menuTreeRef),
      // 非「自定数据权限」不保存部门勾选,保持后端数据干净
      deptIds: form.dataScope === 2 ? collectCheckedKeys(deptTreeRef) : []
    }
    if (!payload.remark) payload.remark = ''

    saving.value = true
    try {
      if (form.id) {
        await updateRole(form.id, payload)
      } else {
        await createRole(payload)
      }
      ElMessage.success('保存成功')
      visible.value = false
      emit('saved')
    } finally {
      saving.value = false
    }
  }

  defineExpose({ open })
</script>

<style scoped>
  .code-input-wrap {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;

    .el-input {
      flex: 1;
    }

    .tip-icon {
      font-size: 14px;
      color: var(--el-text-color-secondary);
      cursor: help;
      flex-shrink: 0;
    }
  }

  .tree-panel {
    width: 100%;
    border: 1px solid var(--el-border-color-lighter);
    border-radius: 4px;
    overflow: hidden;

    .tree-toolbar {
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 6px 10px;
      border-bottom: 1px solid var(--el-border-color-lighter);
      background: var(--el-fill-color-light);

      .el-checkbox {
        margin-right: 8px;
      }
    }

    .tree-body {
      max-height: 240px;
      overflow: auto;
      padding: 4px 2px;
    }
  }
</style>
