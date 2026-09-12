/**
 * 菜单类型共享常量:dir/menu/btn,与后端 menu.type 值域一致。
 *
 * 消费方:列表页搜索下拉、类型列 tag(菜单首页)、菜单弹窗类型 radio。
 * 新增类型需前后端同步(后端路由行为依赖该值域,前端此处仅文案/配色)。
 *
 * 注:该枚举属结构化路由语义(与后端路由加载行为绑定),暂不接字典;
 * 若后续建设 sys_menu_type 字典,以字典替代本文件即可(三处消费点已收敛)。
 */
export interface MenuTypeOption {
  label: string
  value: string
}

export const MENU_TYPE_OPTIONS: MenuTypeOption[] = [
  { label: '目录', value: 'dir' },
  { label: '菜单', value: 'menu' },
  { label: '按钮', value: 'btn' }
]

/** 类型可视化:目录=primary、菜单=success、按钮=warning(对齐 RuoYi)。 */
export const MENU_TYPE_META: Record<
  string,
  { label: string; tag: 'primary' | 'success' | 'warning' }
> = {
  dir: { label: '目录', tag: 'primary' },
  menu: { label: '菜单', tag: 'success' },
  btn: { label: '按钮', tag: 'warning' }
}
