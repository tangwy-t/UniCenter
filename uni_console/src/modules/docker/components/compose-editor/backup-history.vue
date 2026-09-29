<template>
  <div class="backup-history">
    <!-- 未加载过：先点一下拉取列表（避免展开项目行就自动发指令）。 -->
    <ElButton
      v-if="!loaded"
      size="small"
      :loading="loading"
      :disabled="disabled"
      @click="emit('load')"
    >
      历史备份
    </ElButton>

    <!-- 「历史备份(N)」入口：N 来自读文件结果里的备份列表（最多 10 份、按时间倒序）。 -->
    <ElDropdown
      v-else
      trigger="click"
      :disabled="disabled || backups.length === 0"
      @command="onSelect"
    >
      <span class="backup-history__trigger" :title="triggerTitle">
        <ElButton size="small" :disabled="disabled || backups.length === 0">
          历史备份({{ backups.length }})
        </ElButton>
      </span>
      <template #dropdown>
        <ElDropdownMenu>
          <ElDropdownItem v-for="b in backups" :key="b.token" :command="b.token">
            {{ formatBackupTime(b.at) }} · {{ formatSize(b.sizeBytes) }}
            <span class="backup-history__mark">
              {{ b.hash === currentHash ? '与当前一致' : '与当前不同' }}
            </span>
          </ElDropdownItem>
        </ElDropdownMenu>
      </template>
    </ElDropdown>

    <!-- 选中一版后的详情：可核对的信息 + 一键回滚（回滚仍有独立强确认，由调用方弹）。 -->
    <ElDialog v-model="detail.visible" title="备份详情" width="460px">
      <div v-if="detail.backup" class="backup-history__detail">
        <div class="backup-history__row">
          <span class="backup-history__label">备份时间</span>
          <span>{{ formatBackupTime(detail.backup.at) }}</span>
        </div>
        <div class="backup-history__row">
          <span class="backup-history__label">文件体积</span>
          <span>{{ formatSize(detail.backup.sizeBytes) }}</span>
        </div>
        <div class="backup-history__row">
          <span class="backup-history__label">内容标识</span>
          <span class="backup-history__hash">{{ shortHash(detail.backup.hash) }}</span>
        </div>
        <div class="backup-history__row">
          <span class="backup-history__label">与当前文件</span>
          <span>{{ compareText(detail.backup) }}</span>
        </div>
        <!-- 备份正文协议上读不到；把**当前文件**摆在这里，配合上面的同/异结论一起看。 -->
        <div v-if="currentContent" class="backup-history__current">
          <div class="backup-history__current-title">当前配置文件内容</div>
          <pre class="backup-history__current-body">{{ currentContent }}</pre>
        </div>
        <p class="backup-history__note">
          备份的正文不能在页面上直接查看（协议不提供读取备份内容的动作）；上面的信息足以判断是否要回滚，回滚后可在编辑器里查看内容。
        </p>
      </div>
      <template #footer>
        <ElButton @click="detail.visible = false">关闭</ElButton>
        <ElButton
          v-if="canRollback"
          type="danger"
          :disabled="rollingBack || !detail.backup"
          @click="onRollbackClick"
        >
          回滚到此版本…
        </ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
  /**
   * 历史备份（四期，spec §11.5/§11.7）：列表来自 `compose.file:read` 结果里的
   * `backups`（token/hash/size_bytes/at），展示时间、体积与「是否与当前文件一致」，
   * 回滚由调用方执行（`compose.file:write` 带 backup + base_hash，强确认照抄项目名）。
   *
   * 如实标注：协议没有「读备份内容」的动作，故这里不承诺查看备份正文 —— 只给
   * 时间/体积/内容标识与同/异结论，回滚后即可在编辑器里看到内容。
   */
  import { ref } from 'vue'
  import { ElButton, ElDialog, ElDropdown, ElDropdownItem, ElDropdownMenu } from 'element-plus'
  import { formatBackupTime, formatSize, type ComposeBackup } from '../../utils/compose'

  const props = withDefaults(
    defineProps<{
      backups: ComposeBackup[]
      /** 列表是否已加载过（未加载时入口按钮先触发 load）。 */
      loaded?: boolean
      /** 列表加载中。 */
      loading?: boolean
      /** 当前文件的内容标识（用于「与当前一致/不同」的结论）。 */
      currentHash?: string
      /** 当前文件正文（详情里与备份对照着看；备份正文协议上不可读）。 */
      currentContent?: string
      /** 无权限/忙时禁用入口。 */
      disabled?: boolean
      /** 是否渲染「回滚到此版本」按钮（= 有配置编辑权限）。 */
      canRollback?: boolean
      /** 回滚指令在途。 */
      rollingBack?: boolean
    }>(),
    {
      loaded: true,
      loading: false,
      currentHash: '',
      currentContent: '',
      disabled: false,
      canRollback: false,
      rollingBack: false
    }
  )

  const emit = defineEmits<{
    (e: 'load'): void
    (e: 'rollback', backup: ComposeBackup): void
  }>()

  const detail = ref<{ visible: boolean; backup: ComposeBackup | null }>({
    visible: false,
    backup: null
  })

  const triggerTitle = props.backups.length ? '查看历史备份' : '暂无备份（保存一次配置后会出现）'

  function onSelect(token: string | number | object): void {
    const backup = props.backups.find((b) => b.token === String(token)) ?? null
    detail.value = { visible: true, backup }
  }

  function compareText(backup: ComposeBackup): string {
    if (!props.currentHash) return '无法比对（当前内容未知）'
    return backup.hash === props.currentHash ? '一致（无需回滚）' : '不同'
  }

  function shortHash(hash: string): string {
    return hash ? hash.slice(0, 10) : '未知'
  }

  function onRollbackClick(): void {
    if (!detail.value.backup) return
    detail.value.visible = false
    emit('rollback', detail.value.backup)
  }
</script>

<style lang="scss" scoped>
  .backup-history {
    display: inline-flex;

    &__mark {
      margin-left: 8px;
      color: var(--el-text-color-secondary);
      font-size: 12px;
    }

    &__detail {
      font-size: 13px;
    }

    &__row {
      display: flex;
      gap: 12px;
      padding: 4px 0;
    }

    &__label {
      flex: none;
      width: 72px;
      color: var(--el-text-color-secondary);
    }

    &__hash {
      font-family: var(--art-font-family-mono, monospace);
    }

    &__note {
      margin: 10px 0 0;
      color: var(--el-text-color-secondary);
      font-size: 12px;
      line-height: 1.6;
    }

    &__current {
      margin-top: 10px;

      &-title {
        margin-bottom: 4px;
        color: var(--el-text-color-secondary);
        font-size: 12px;
      }

      &-body {
        max-height: 220px;
        margin: 0;
        padding: 8px;
        overflow: auto;
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 6px;
        background: var(--el-fill-color-lighter);
        font-family: var(--art-font-family-mono, monospace);
        font-size: 12px;
        line-height: 1.6;
        white-space: pre-wrap;
        word-break: break-all;
      }
    }
  }
</style>
