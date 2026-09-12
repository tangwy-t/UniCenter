<template>
  <el-dialog
    v-model="visible"
    :title="form.id ? '编辑菜单' : '新增菜单'"
    width="680px"
    :close-on-click-modal="false"
    append-to-body
    destroy-on-close
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="92px">
      <el-row :gutter="12">
        <el-col :span="24">
          <el-form-item label="上级菜单">
            <el-tree-select
              v-model="form.parentId"
              :data="parentTree"
              :props="{ label: 'label', children: 'children' }"
              node-key="value"
              check-strictly
              clearable
              placeholder="选择上级菜单"
              style="width: 100%"
            />
          </el-form-item>
        </el-col>

        <el-col :span="24">
          <el-form-item label="菜单类型" prop="type">
            <el-radio-group v-model="form.type">
              <el-radio v-for="t in MENU_TYPE_OPTIONS" :key="t.value" :value="t.value">{{
                t.label
              }}</el-radio>
            </el-radio-group>
          </el-form-item>
        </el-col>

        <template v-if="form.type !== 'btn'">
          <el-col :span="12">
            <el-form-item label="菜单图标">
              <ArtIconPicker
                v-model="form.icon"
                :width="440"
                :height="320"
                placeholder="点击选择图标"
              />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="显示排序" prop="sort">
              <el-input-number
                v-model="form.sort"
                controls-position="right"
                :min="0"
                style="width: 100%"
              />
            </el-form-item>
          </el-col>
        </template>

        <el-col :span="form.type === 'btn' ? 24 : 12">
          <el-form-item label="菜单名称" prop="name">
            <el-input v-model="form.name" placeholder="请输入菜单名称" maxlength="64" />
          </el-form-item>
        </el-col>

        <template v-if="form.type !== 'btn'">
          <el-col :span="12">
            <el-form-item prop="path">
              <template #label>
                <span class="inline-flex items-center whitespace-nowrap">
                  路由路径
                  <el-tooltip content="访问的路由地址，如 /system/user；目录可留空" placement="top">
                    <el-icon class="ml-0.5"><QuestionFilled /></el-icon>
                  </el-tooltip>
                </span>
              </template>
              <el-input v-model="form.path" placeholder="请输入路由路径（目录可留空）" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item prop="component">
              <template #label>
                <span class="inline-flex items-center whitespace-nowrap">
                  组件路径
                  <el-tooltip content="访问的组件路径，如 system/user/index" placement="top">
                    <el-icon class="ml-0.5"><QuestionFilled /></el-icon>
                  </el-tooltip>
                </span>
              </template>
              <el-input v-model="form.component" placeholder="请输入组件路径" />
            </el-form-item>
          </el-col>
        </template>

        <template v-if="form.type !== 'dir'">
          <el-col :span="12">
            <el-form-item prop="perms">
              <template #label>
                <span class="inline-flex items-center whitespace-nowrap">
                  权限标识
                  <el-tooltip content="权限标识，如 system:user:list" placement="top">
                    <el-icon class="ml-0.5"><QuestionFilled /></el-icon>
                  </el-tooltip>
                </span>
              </template>
              <el-input v-model="form.perms" placeholder="请输入权限标识" maxlength="100" />
            </el-form-item>
          </el-col>
        </template>

        <el-col :span="12">
          <el-form-item label="显示状态">
            <el-radio-group v-model="form.visible">
              <el-radio v-for="opt in visibleOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </el-radio>
            </el-radio-group>
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="菜单状态">
            <el-radio-group v-model="form.status">
              <el-radio v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </el-radio>
            </el-radio-group>
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
  import { computed, reactive, ref } from 'vue'
  import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
  import { QuestionFilled } from '@element-plus/icons-vue'
  import ArtButtonTable from '@/components/core/forms/art-button-table/index.vue'
  import ArtIconPicker from '@/components/core/forms/art-icon-picker/index.vue'
  import { useDict } from '@/hooks/core/useDict'
  import { MENU_TYPE_OPTIONS } from '../constants'
  import { fetchMenus, createMenu, updateMenu } from '../api'

  const emit = defineEmits<{ (e: 'saved'): void }>()
  const { ensure: ensureStatus, options: statusOptions } = useDict('sys_menu_status', {
    numeric: true
  })
  const { ensure: ensureVisible, options: visibleOptions } = useDict('sys_show_hide', {
    numeric: true
  })

  const visible = ref(false)
  const formRef = ref<FormInstance>()
  const menus = ref<Api.System.Menu[]>([])
  const form = reactive<Api.System.MenuForm>({
    id: undefined,
    parentId: undefined,
    name: '',
    type: 'menu',
    perms: '',
    path: '',
    component: '',
    icon: '',
    sort: undefined,
    visible: 1,
    status: 1
  })

  // 校验规则：名称必填；菜单(menu)类型组件路径必须、路由路径必填；目录(dir)路由路径非必填。
  const rules = computed<FormRules>(() => ({
    name: [{ required: true, message: '菜单名称不能为空', trigger: 'blur' }],
    path:
      form.type === 'menu'
        ? [{ required: true, message: '路由路径不能为空', trigger: 'blur' }]
        : [],
    component:
      form.type === 'menu' ? [{ required: true, message: '组件路径不能为空', trigger: 'blur' }] : []
  }))

  // 上级菜单树：排除按钮节点,顶部提供“顶级菜单”虚拟节点(value 与服务端 parentId=0 对应)。
  const parentTree = computed(() => {
    const toNode = (m: Api.System.Menu): any => ({
      value: m.id,
      label: m.name,
      children: m.children?.filter((c) => c.type !== 'btn').map(toNode) ?? []
    })
    return [
      { value: '0', label: '顶级菜单' },
      ...menus.value.filter((m) => m.type !== 'btn').map(toNode)
    ]
  })

  async function open(row?: Api.System.Menu, parentId?: string) {
    await Promise.all([ensureStatus(), ensureVisible()])
    menus.value = await fetchMenus()
    Object.assign(
      form,
      row ?? {
        id: undefined,
        parentId: parentId ?? undefined,
        name: '',
        type: 'menu',
        perms: '',
        path: '',
        component: '',
        icon: '',
        sort: undefined,
        visible: 1,
        status: 1
      }
    )
    visible.value = true
  }

  async function onSave() {
    if (!formRef.value) return
    await formRef.value.validate(async (valid) => {
      if (!valid) return
      const payload: Api.System.MenuForm = { ...form }
      // 上级菜单为空/被清空 → 显式提交 parentId=0 移到顶级目录。
      if (!payload.parentId) payload.parentId = 0
      // 排序留空 → 不提交该字段,服务端追加到同级末尾(显式填 0 则为置顶)。
      if (payload.sort === undefined || payload.sort === null) delete payload.sort
      if (form.id) {
        await updateMenu(form.id, payload)
      } else {
        await createMenu(payload)
      }
      ElMessage.success('已保存')
      visible.value = false
      emit('saved')
    })
  }

  defineExpose({ open })
</script>
