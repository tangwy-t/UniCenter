// @vitest-environment jsdom
/**
 * 守卫：合成事件不受**宿主墙钟回拨**影响（Vue 事件 invoker 的时间戳门槛）。
 *
 * 机制与现场证据在 src/test/setup.ts 的注释里；这里用**模拟回拨**把它钉住 ——
 *   - 撤掉 setupFiles / 改坏补丁：本文件必红（两条用例各自复现一条实测失败路径）；
 *   - 保留补丁：墙钟怎么回拨，合成事件都照常送达处理器。
 *
 * 两条用例对应放大复现里实测到的两族：
 *   1) trigger('click') 被吞 —— 现场「按钮可点、弹窗不开、sendDockerCmd 0 调用」；
 *   2) setValue 的 input 事件被吞 —— 现场「输入框里字在、v-model 没动、
 *      等 enabled 的 waitUntil 兜到超时」。
 *
 * 为什么模拟回拨而不是改系统时钟：回拨是宿主（WSL2 时间同步）行为，测试只能
 * 在进程内复刻它的**效果**——把 Date.now() 打回 2.4s 前（与实测回拨量同档）。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref } from 'vue'
import { mount } from '@vue/test-utils'

/** 与实测回拨同档（~2.4s）：跨过「监听器创建时刻」这道门槛即可。 */
const JUMP_BACK_MS = 2441

afterEach(() => {
  vi.restoreAllMocks()
})

/** 挂载时监听器就位（attached = 真墙钟）；之后才把墙钟往回拨。 */
function jumpClockBack() {
  const back = Date.now() - JUMP_BACK_MS
  vi.spyOn(Date, 'now').mockReturnValue(back)
}

const ClickProbe = defineComponent({
  name: 'ClockGuardClickProbe',
  setup() {
    const hits = ref(0)
    return () => h('button', { onClick: () => hits.value++ }, `hits=${hits.value}`)
  }
})

const InputProbe = defineComponent({
  name: 'ClockGuardInputProbe',
  setup() {
    const model = ref('')
    return () =>
      h('div', [
        h('input', {
          value: model.value,
          onInput: (e: Event) => {
            model.value = (e.target as HTMLInputElement).value
          }
        }),
        // 断言看**模型**（渲染出来的这行文字），不看 input.value ——
        // setValue 会直接写 DOM 属性，事件被吞时 DOM 值照样在，只有模型没动。
        h('span', { class: 'model' }, model.value)
      ])
  }
})

describe('事件时间戳守卫豁免（墙钟回拨）', () => {
  it('回拨后 trigger 的点击仍送达处理器', async () => {
    const w = mount(ClickProbe)
    jumpClockBack()
    await w.find('button').trigger('click')
    expect(w.text()).toBe('hits=1')
  })

  it('回拨后 setValue 的 input 事件仍更新 v-model', async () => {
    const w = mount(InputProbe)
    jumpClockBack()
    await w.find('input').setValue('nginx:latest')
    expect(w.find('.model').text()).toBe('nginx:latest')
  })
})
