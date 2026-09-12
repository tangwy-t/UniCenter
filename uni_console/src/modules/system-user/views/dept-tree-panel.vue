<!--
  部门树面板（用户管理页侧栏/移动端抽屉复用）
  - 输入过滤：按部门名称实时筛选
  - 「全部用户」：清除部门过滤
  - 再次点击已选中节点：取消选中
-->
<template>
  <div class="dept-tree-panel">
    <div class="panel-title">
      <span class="flex items-center gap-1.5">
        <ArtSvgIcon icon="ri:folder-user-line" class="text-g-600 text-[16px]" />
        <span>组织机构</span>
      </span>
      <div class="panel-title-btn" title="刷新组织树" @click="emit('refresh')">
        <ArtSvgIcon
          icon="ri:refresh-line"
          class="text-g-500 text-[14px]"
          :class="loading ? 'animate-spin' : ''"
        />
      </div>
    </div>

    <ElInput
      v-model="filterText"
      class="panel-search"
      placeholder="请输入部门名称"
      clearable
      :prefix-icon="Search"
    />

    <div class="all-node" :class="{ active: currentKey === null }" @click="clearSelect">
      <ArtSvgIcon icon="ri:apps-2-line" class="text-[15px]" />
      <span>全部用户</span>
      <span v-if="currentKey === null" class="all-node-check">
        <ArtSvgIcon icon="ri:check-line" class="text-[12px]" />
      </span>
    </div>

    <ElScrollbar v-loading="loading" class="panel-scroll">
      <ElTree
        v-if="data.length"
        ref="treeRef"
        :data="data"
        node-key="id"
        :props="{ label: 'name', children: 'children' }"
        default-expand-all
        :expand-on-click-node="false"
        :highlight-current="true"
        :filter-node-method="filterNode"
        @node-click="onNodeClick"
      >
        <template #default="{ data: node }">
          <span class="tree-node-label">
            <ArtSvgIcon
              :icon="node.children?.length ? 'ri:folder-3-line' : 'ri:folder-line'"
              class="text-[14px]"
            />
            <span>{{ node.name }}</span>
          </span>
        </template>
      </ElTree>
      <ElEmpty v-if="!loading && !data.length" description="暂无部门数据" :image-size="64" />
    </ElScrollbar>
  </div>
</template>

<script setup lang="ts">
  import { ref, watch } from 'vue'
  import { Search } from '@element-plus/icons-vue'
  import type { ElTree } from 'element-plus'
  import ArtSvgIcon from '@/components/core/base/art-svg-icon/index.vue'

  defineOptions({ name: 'DeptTreePanel' })

  interface Props {
    /** 部门树数据 */
    data: Api.System.Dept[]
    /** 加载状态 */
    loading?: boolean
  }

  // 无需接收返回值:模板中 data / loading 由 defineProps 自动解包。
  // 此前写成 `const props = withDefaults(...)` 但从未访问 props,是个未使用绑定。
  withDefaults(defineProps<Props>(), {
    loading: false
  })

  const emit = defineEmits<{
    (e: 'select', dept: Api.System.Dept | null): void
    (e: 'refresh'): void
  }>()

  const treeRef = ref<InstanceType<typeof ElTree>>()
  const filterText = ref('')
  const currentKey = ref<string | null>(null)

  /** 按名称过滤节点（大小写不敏感） */
  function filterNode(value: string, data: Record<string, any>) {
    if (!value) return true
    const name = (data?.name ?? '') as string
    return name.toLowerCase().includes(value.toLowerCase())
  }

  watch(filterText, (val) => {
    treeRef.value?.filter(val)
  })

  function onNodeClick(node: Api.System.Dept) {
    const id = String(node.id)
    if (currentKey.value === id) {
      // 再次点击已选中节点 → 取消过滤
      clearSelect()
      return
    }
    currentKey.value = id
    treeRef.value?.setCurrentKey(id)
    emit('select', node)
  }

  function clearSelect() {
    currentKey.value = null
    treeRef.value?.setCurrentKey(undefined)
    emit('select', null)
  }

  defineExpose({ clearSelect })
</script>

<style lang="scss" scoped>
  .dept-tree-panel {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;

    .panel-title {
      display: flex;
      align-items: center;
      justify-content: space-between;
      margin-bottom: 12px;
      font-size: 14px;
      font-weight: 600;
      color: var(--el-text-color-primary);

      .panel-title-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 26px;
        height: 26px;
        border-radius: 6px;
        cursor: pointer;
        transition: background-color 0.2s;

        &:hover {
          background-color: var(--el-fill-color-light);
        }
      }
    }

    .panel-search {
      margin-bottom: 10px;
    }

    .all-node {
      display: flex;
      align-items: center;
      gap: 6px;
      padding: 7px 8px;
      margin-bottom: 6px;
      border-radius: 6px;
      font-size: 13px;
      color: var(--el-text-color-primary);
      cursor: pointer;
      transition:
        background-color 0.2s,
        color 0.2s;

      &:hover {
        background-color: var(--el-fill-color-light);
      }

      &.active {
        color: var(--el-color-primary);
        background-color: var(--el-color-primary-light-9);
        font-weight: 500;
      }

      .all-node-check {
        display: flex;
        align-items: center;
        margin-left: auto;
      }
    }

    .panel-scroll {
      flex: 1;
      min-height: 0;
    }

    :deep(.el-tree) {
      --el-tree-node-hover-bg-color: var(--el-fill-color-light);
      background: transparent;
      font-size: 13px;

      .tree-node-label {
        display: inline-flex;
        align-items: center;
        gap: 5px;
        color: var(--el-text-color-regular);

        .art-svg-icon {
          color: var(--el-text-color-secondary);
        }
      }

      .el-tree-node.is-current > .el-tree-node__content .tree-node-label {
        color: var(--el-color-primary);
        font-weight: 500;

        .art-svg-icon {
          color: var(--el-color-primary);
        }
      }
    }
  }
</style>
