import App from './App.vue'
import { createApp } from 'vue'
import { initStore } from './store'                 // Store
import { initRouter } from './router'             // Router
import '@styles/core/tailwind.css'                  // tailwind
import '@styles/index.scss'                         // 样式
import '@utils/sys/console.ts'                      // 控制台输出内容
import { setupGlobDirectives } from './directives'
import { setupErrorHandle } from './utils/sys/error-handle'

document.addEventListener(
  'touchstart',
  function () {},
  { passive: false }
)

async function bootstrap() {
  // 注册离线图标集（ri: 全量）。动态导入使其打成独立 chunk，避免撑大首屏主包，
  // 在真正挂载前完成图标注册，保证任意 ri: 图标离线可渲染。
  await import('./utils/ui/iconify-loader')

  const app = createApp(App)
  initStore(app)
  initRouter(app)
  setupGlobDirectives(app)
  setupErrorHandle(app)

  app.mount('#app')
}

bootstrap()
