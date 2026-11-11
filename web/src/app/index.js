// 组装层：把各领域模块挂到同一个 ctx 上，供 App.vue 一次性 provide 给页面组件。
// 各模块只通过 ctx 读写共享状态与互相调用，模块之间没有直接的 import 依赖，
// 因此新增领域只要再挂一个 createXxx(ctx) 即可，不需要改动已有模块。
import { createState } from './state.js'
import { createClient } from './client.js'
import { createLookup } from './lookup.js'
import { createVideos } from './videos.js'
import { createCatalog } from './catalog.js'
import { createDetail } from './detail.js'
import { createSources } from './sources.js'
import { createTasks } from './tasks.js'
import { createSettings } from './settings.js'
import { createUsers } from './users.js'
import { createAuth } from './auth.js'
import { createShell } from './shell.js'

export function createApp() {
  // 顺序有要求：状态 → 客户端 → 展示工具 → 资源库（用到展示工具）→ 其余领域 → 外壳（依赖全部加载函数）。
  const ctx = createState()
  Object.assign(ctx, createClient(ctx))
  Object.assign(ctx, createLookup(ctx))
  Object.assign(ctx, createVideos(ctx))
  Object.assign(ctx, createCatalog(ctx))
  Object.assign(ctx, createDetail(ctx))
  Object.assign(ctx, createSources(ctx))
  Object.assign(ctx, createTasks(ctx))
  Object.assign(ctx, createSettings(ctx))
  Object.assign(ctx, createUsers(ctx))
  Object.assign(ctx, createAuth(ctx))
  Object.assign(ctx, createShell(ctx))
  return ctx
}
