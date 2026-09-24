import { loadEnv } from 'vite'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import VueDevTools from 'vite-plugin-vue-devtools'
import path from 'path'
import { fileURLToPath } from 'url'
import viteCompression from 'vite-plugin-compression'
import Components from 'unplugin-vue-components/vite'
import AutoImport from 'unplugin-auto-import/vite'
import ElementPlus from 'unplugin-element-plus/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import tailwindcss from '@tailwindcss/vite'

/**
 * 客户端**编译期**变量（VITE_*）的唯一来源是仓库根的 `.env`。
 *
 * 为什么要在这里显式读根目录：`import.meta.env.VITE_API_PREFIX` 在源码里被内联成
 * **字面量**（见 src/api/auth.ts 的 `${PREFIX}/login`），而这台开发机上手工
 * `pnpm build` 时如果没有这些变量，产物里会写成 `"undefined/login"` ——
 * **构建成功、页面能开、只有点登录才 404**，正是那种最难发现的形态。
 * 2026-09-24 就是这么把一份坏 dist 发到生产上的，故这里补两道：
 *   1) 读根 .env（与 compose 的 build args 同源）作为配置期变量来源；
 *   2) **缺关键变量直接让构建失败**（见下面 REQUIRED_CLIENT_ENV）。
 */
const REQUIRED_CLIENT_ENV = ['VITE_API_PREFIX', 'VITE_API_URL', 'VITE_BASE_URL'] as const

export default ({ mode, command }: { mode: string; command: 'serve' | 'build' }) => {
  const root = process.cwd()
  // 根 .env 优先（单一事实源），本目录的同名文件可覆盖（dev 的代理地址就在那儿）。
  const repoEnv = loadEnv(mode, path.resolve(root, '..'))
  const env = { ...repoEnv, ...loadEnv(mode, root) }
  const { VITE_VERSION, VITE_PORT, VITE_BASE_URL, VITE_API_URL, VITE_API_PROXY_URL } = env

  // **守卫必须按「源码内联时实际会拿到什么」来判**：`import.meta.env.VITE_*` 由 Vite 从
  // `envDir`（默认本目录）的 .env* 文件 + 真实 `process.env` 两处取值。
  // 只判上面那个合并后的 env 是不够的 —— 它含根 .env，而根 .env 并**不**参与源码内联，
  // 于是守卫会在「变量其实缺失」时照样放行（第一版就是这么写的，实测漏拦）。
  const inlined = loadEnv(mode, root)
  const effective = (k: string): string | undefined => process.env[k] || inlined[k]
  const missing = REQUIRED_CLIENT_ENV.filter((k) => !effective(k))
  if (missing.length > 0) {
    throw new Error(
      `缺少客户端编译期变量：${missing.join(', ')}。\n` +
        `  它们必须来自仓库根的 .env（与 docker compose 的 build args 同源）。\n` +
        `  · 本地构建请用 'make uni_console-build'（会自动 source 根 .env），或\n` +
        `    'set -a; . ../.env; set +a; pnpm build'；\n` +
        `  · 这样产出的 dist 才不会把 URL 内联成 "undefined/login"。`
    )
  }

  console.log(`🚀 API_URL = ${VITE_API_URL}`)
  console.log(`🚀 VERSION = ${VITE_VERSION}`)

  return defineConfig({
    define: {
      __APP_VERSION__: JSON.stringify(VITE_VERSION)
    },
    base: VITE_BASE_URL,
    server: {
      port: Number(VITE_PORT),
      proxy: {
        '/api': {
          target: VITE_API_PROXY_URL,
          changeOrigin: true,
          ws: true // WebSocket 升级转发（/api/v1/ws）
        }
      },
      host: true
    },
    // 路径别名
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
        '@views': resolvePath('src/views'),
        '@imgs': resolvePath('src/assets/images'),
        '@icons': resolvePath('src/assets/icons'),
        '@utils': resolvePath('src/utils'),
        '@stores': resolvePath('src/store'),
        '@styles': resolvePath('src/assets/styles')
      }
    },
    build: {
      target: 'es2015',
      outDir: 'dist',
      chunkSizeWarningLimit: 2000,
      minify: 'terser',
      terserOptions: {
        compress: {
          drop_console: true,
          drop_debugger: true
        }
      },
      dynamicImportVarsOptions: {
        warnOnError: true,
        exclude: [],
        include: ['src/views/**/*.vue']
      }
    },
    plugins: [
      // Vue DevTools —— 仅开发模式启用（vite build / vitest 自动排除）
      ...(command === 'serve' && mode !== 'test' ? [VueDevTools()] : []),
      vue(),
      tailwindcss(),
      // 自动按需导入 API
      AutoImport({
        imports: ['vue', 'vue-router', 'pinia', '@vueuse/core'],
        dts: 'src/types/import/auto-imports.d.ts',
        resolvers: [ElementPlusResolver()],
        eslintrc: {
          enabled: true,
          filepath: './.auto-import.json',
          globalsPropValue: true
        }
      }),
      // 自动按需导入组件
      Components({
        dts: 'src/types/import/components.d.ts',
        resolvers: [ElementPlusResolver()]
      }),
      // 按需定制主题配置
      ElementPlus({
        useSource: true
      }),
      // gzip 压缩
      viteCompression({
        verbose: false,
        disable: false,
        algorithm: 'gzip',
        ext: '.gz',
        threshold: 10240,
        deleteOriginFile: false
      })
    ],
    // Vitest:element-plus(含 unplugin-element-plus 注入的 theme-chalk/src scss 副作用导入)
    // 位于 node_modules,SSR/node 默认外部化会交给原生加载器解析 .scss 直接报错;
    // 内联进 vite 编译管线即可正常处理样式导入。
    test: {
      server: {
        deps: {
          inline: [/element-plus/]
        }
      }
    },
    // 依赖预构建
    optimizeDeps: {
      include: [
        'echarts/core',
        'echarts/charts',
        'echarts/components',
        'echarts/renderers',
        'xlsx',
        'file-saver',
        'element-plus/es',
        'element-plus/es/components/*/style/css',
        'element-plus/es/components/*/style/index'
      ]
    },
    css: {
      preprocessorOptions: {
        scss: {
          additionalData: `
            @use "@styles/core/el-light.scss" as *; 
            @use "@styles/core/mixin.scss" as *;
          `
        }
      },
      postcss: {
        plugins: [
          {
            postcssPlugin: 'internal:charset-removal',
            AtRule: {
              charset: (atRule) => {
                if (atRule.name === 'charset') {
                  atRule.remove()
                }
              }
            }
          }
        ]
      }
    }
  })
}

function resolvePath(paths: string) {
  return path.resolve(__dirname, paths)
}
