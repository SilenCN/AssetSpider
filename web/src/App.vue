<script setup>
// 应用外壳：登录页 / 侧边栏 / 主内容区与全局弹窗的骨架。
// 业务状态与行为按领域拆到 ./app/ 下（state / videos / catalog / detail / sources /
// tasks / settings / users / auth / shell），本文件只做装配与布局：
// createApp() 返回同一个 ctx 对象，既 provide 给页面组件，也解构给本文件模板使用。
import { provide } from 'vue'
import { createApp } from './app/index.js'
import SourcesView from './views/SourcesView.vue'
import CategoriesView from './views/CategoriesView.vue'
import VideosView from './views/VideosView.vue'
import SettingsView from './views/SettingsView.vue'
import VideoDetailView from './views/VideoDetailView.vue'
import JobsView from './views/JobsView.vue'
import UsersView from './views/UsersView.vue'

const app = createApp()
provide('app', app)

// 模板直接引用的状态与动作（解构出来的仍是原始 ref / computed / 函数）。
const {
  authReady, me, loginForm, loginBusy, loginError, initialized, doLogin, doLogout,
  isAdmin, currentUsername, token, clearAuth, setAuth, pwdModal, pwdForm, doChangePassword,
  tab, switchTab, navItems, currentPageTitle, sidebarCollapsed, sidebarMobileOpen, toggleSidebar,
  health, sources, capacity, settings, settingsForm, settingsDirty, settingsSaving, saveSettings,
  error, notice, detail, detailLoading, loadVideos,
  form, editing, sourceOpen, openSourceForm, closeSourceForm, editSource, saveSource,
  removeSource, clearSourceData, toggleSource, sourcePayload, emptyForm, resetForm,
  cronFeedback, cronNextText, exportSources, importOpen, importBusy, importFileName,
  importPreview, importFileEl, importPreviewShown, pickImportFile, onImportFile,
  closeImport, confirmImport, onEsc, refresh,
  subForm, subOpen, subBusy, subEditing, closeSubForm, saveSubscription,
  userForm, userEditing, userFormOpen, openUserForm, closeUserForm, resetUserForm,
  editUser, saveUser, toggleUser, removeUser, visibleLabel, users,
  activeTaskOf, queuedCountOf, crawlAll, startTask, pauseTask, resumeTask, retryTask, rerunTask,
  modeLabel, statusLabel, formatTime, short, stat, jobMessage, barWidth, isIndeterminate,
  fmtEta, taskStatusText, searchVideos, onFilterChange, clearAllFilters,
  showCategoryVideos, toggleAllCats, toggleCat, isCatCollapsed, catKey, catTotal, catVideoCount,
  categoryForest, catSelectLabel, siteNameOf, categoryName, visibleVideoTotal,
  videoQuery, videoSource, videoCategory, videoSort,
  videos, visibleCategories, hasActiveFilters,
  videoEnd, videoLoading, videoLoadingMore, videoSentinel, videoPage, videoSize,
  fetchVideos, loadMoreVideos, kickSentinel, loadJobs, loadTasks,
  loadSources, loadCategories, loadCategoryTree, loadUsers, loadHealth, loadSettings,
  request, jsonOptions, notify,
  fieldMeta, detailTab, onlyNonEmpty, loadFieldMeta, hasValue, nonEmptyCount, groupedFields,
  extraPairs, playGroups, posterOf, markPicFailed, episodeLabel, scoreOf, metaLine, tagListOf,
  playerEl, activeGroup, activeEp, currentGroup, currentEpisodes, currentEpisode, currentUrl,
  detailSource, parseEnabled, useFrame, isHls, isDirectVideo, isUnknownStream, canPlay,
  playError, onVideoLoaded, hlsErrorText, videoErrorText, onVideoError, destroyPlayer,
  mountPlayer, chooseGroup, chooseEp, switchDetailTab, closeDetail, openDetail, loadHls,
  collectTabs, refreshCollectData, onVisible, bootstrap,
} = app
</script>

<template>
  <div v-if="!authReady" class="boot">正在检查登录状态…</div>

  <div v-else-if="!me" class="login-wrap">
    <div class="login-card">
      <h1>AssetSpider</h1>
      <p class="muted">视频站点采集 · JSON 接口 · 全量 / 增量 · 全字段入库</p>
      <form @submit.prevent="doLogin">
        <input v-model="loginForm.username" placeholder="用户名" autocomplete="username" required />
        <input v-model="loginForm.password" type="password" placeholder="密码" autocomplete="current-password" required />
        <button type="submit" :disabled="loginBusy">
          {{ loginBusy ? '登录中…' : (initialized ? '登录' : '注册并登录') }}
        </button>
      </form>
      <p v-if="!initialized" class="hint">
        第一个登录的账号将<strong>自动注册为管理员</strong>，拥有全部权限；之后可创建用户并分配可见站点。
      </p>
      <p v-if="loginError" class="error">{{ loginError }}</p>
    </div>
  </div>


  <template v-else>
  <div class="app-shell">
    <!-- 移动端抽屉遮罩 -->
    <div v-if="sidebarMobileOpen" class="sidebar-backdrop" @click="sidebarMobileOpen = false"></div>

    <aside class="sidebar" :class="{ collapsed: sidebarCollapsed, 'mobile-open': sidebarMobileOpen }">
      <div class="sidebar-head">
        <span class="brand-mark">AS</span>
        <div class="brand-text">
          <h1>AssetSpider</h1>
          <p>视频站点采集</p>
        </div>
      </div>

      <nav class="side-nav">
        <button v-for="item in navItems" :key="item.key"
                class="nav-item" :class="{ active: tab === item.key, primary: item.primary }"
                :title="item.label" @click="switchTab(item.key)">
          <svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path :d="item.icon" />
          </svg>
          <span class="nav-label">{{ item.label }}</span>
          <span v-if="item.count" class="nav-count">{{ item.count }}</span>
        </button>
      </nav>

      <div class="side-overview">
        <div class="ov">
          <span class="ov-label">可见影片</span>
          <b class="ov-value">{{ visibleVideoTotal }}</b>
          <span class="ov-sub">按站点影片数快照累加</span>
        </div>
        <div class="ov">
          <span class="ov-label">采集任务</span>
          <b class="ov-value">{{ capacity.running }}<span class="ov-unit">/ {{ capacity.maxConcurrent }}</span></b>
          <span class="ov-sub">运行中 · 排队 {{ capacity.queued }}</span>
        </div>
      </div>

      <div class="side-foot">
        <span class="db-chip" :title="health.database || ''">DB {{ health.database || '加载中…' }}</span>

        <!-- 账号与操作区：原在顶栏右上角，现移到导航栏最底部 -->
        <div class="side-account">
          <div class="acct-head" :title="sidebarCollapsed ? '展开侧边栏查看账号操作' : currentUsername"
               @click="sidebarCollapsed && toggleSidebar()">
            <span class="acct-avatar">{{ (currentUsername || '?').slice(0, 1).toUpperCase() }}</span>
            <span class="acct-text">
              <b class="acct-name">{{ currentUsername || '未登录' }}</b>
              <span class="badge" :class="isAdmin ? 'on' : 'off'">{{ isAdmin ? '管理员' : '普通用户' }}</span>
            </span>
          </div>
          <div class="acct-actions">
            <button class="mini ghost" @click="pwdModal = true">修改密码</button>
            <button class="mini ghost" @click="doLogout">退出登录</button>
          </div>
        </div>
      </div>

      <button class="sidebar-toggle" @click="toggleSidebar"
              :title="sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
             stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path :d="sidebarCollapsed ? 'M9 6l6 6-6 6' : 'M15 6l-6 6 6 6'" />
        </svg>
        <span>收起侧边栏</span>
      </button>
    </aside>

    <div class="main-area">
      <header class="topbar">
        <button class="hamburger" @click="sidebarMobileOpen = !sidebarMobileOpen" aria-label="打开菜单">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
               stroke-linecap="round" aria-hidden="true"><path d="M3 6h18M3 12h18M3 18h18" /></svg>
        </button>
        <div class="topbar-title"><span>AssetSpider</span><b>{{ currentPageTitle }}</b></div>
      </header>

      <main class="page">
        <div v-if="notice" class="notice" :class="notice.type">{{ notice.text }}</div>
        <p v-if="error" class="error">{{ error }}</p>

        <!-- 影片详情是独立页面，占据整个主内容区；按 id 拉详情期间显示占位 -->
        <VideoDetailView v-if="detail" />
        <div v-else-if="detailLoading" class="card detail-loading">
          <span class="spinner"></span><span>正在加载影片详情…</span>
        </div>
        <template v-else>
          <SourcesView v-if="tab === 'sources'" />
          <CategoriesView v-else-if="tab === 'categories'" />
          <VideosView v-else-if="tab === 'videos'" />
          <JobsView v-else-if="tab === 'jobs'" />
          <SettingsView v-else-if="tab === 'settings' && isAdmin" />
          <UsersView v-else-if="tab === 'users' && isAdmin" />
        </template>
      </main>
    </div>
  </div>

    <!-- 站点配置导入确认（仅管理员） -->
    <div v-if="importOpen" class="modal-mask" @click.self="closeImport">
      <div class="modal narrow">
        <div class="modal-head">
          <div>
            <h3>导入站点配置</h3>
            <p class="muted">{{ importFileName }} · 共 {{ importPreview.length }} 条配置</p>
          </div>
          <button class="ghost" @click="closeImport">关闭</button>
        </div>
        <div class="modal-body">
          <p class="hint">按<strong>站点名称</strong>匹配：同名配置会被更新（保留站点 ID、采集状态与增量游标），不同名的新建，因此重复导入不会产生重复站点。单条配置校验失败不影响其它条目。支持导出文件、站点数组、单条站点对象三种 JSON。</p>
          <div class="import-list">
            <div v-for="(s, i) in importPreviewShown" :key="i" class="import-row">
              <span class="mono muted">#{{ i + 1 }}</span>
              <b>{{ s.name || '（未命名）' }}</b>
              <span class="muted">{{ s.apiBase || '（缺少接口地址）' }}</span>
            </div>
            <div v-if="importPreview.length > importPreviewShown.length" class="import-row muted">…… 还有 {{ importPreview.length - importPreviewShown.length }} 条未列出</div>
          </div>
        </div>
        <div class="modal-actions">
          <button :disabled="importBusy" @click="confirmImport">{{ importBusy ? '导入中…' : '确认导入' }}</button>
          <button class="ghost" @click="closeImport">取消</button>
        </div>
      </div>
    </div>

    <!-- 站点配置弹窗（新增 / 编辑共用，仅管理员） -->
    <div v-if="sourceOpen && isAdmin" class="modal-mask" @click.self="closeSourceForm">
      <div class="modal narrow">
        <div class="modal-head">
          <div>
            <h3>{{ editing ? '编辑站点' : '新增采集站点' }}</h3>
            <p class="muted">苹果CMS V10 Provide 接口：<code>api.php/provide/vod/?ac=videolist</code></p>
          </div>
          <button class="ghost" @click="closeSourceForm">关闭</button>
        </div>
        <form class="modal-body sf-form" @submit.prevent="saveSource">
          <label class="sf-item">
            <span>站点名称 *</span>
            <input v-model="form.name" placeholder="例：某某资源站" required />
          </label>
          <label class="sf-item">
            <span>附加参数</span>
            <input v-model="form.extraParams" placeholder="如 t=1&amp;wd=电影（可留空）" />
          </label>
          <label class="sf-item full">
            <span>JSON 接口地址 *</span>
            <input v-model="form.apiBase" placeholder="https://site.com/api.php/provide/vod/" required />
          </label>

          <p class="sf-group full">播放增强<span class="muted">只影响播放时的地址，不改动入库的原始地址</span></p>
          <label class="sf-item full">
            <span>视频解析地址</span>
            <input v-model="form.parseApi" placeholder="https://jx.example.com/?url=（可留空，支持 {url} 占位符）" />
          </label>
          <label class="sf-item full">
            <span>视频播放域名</span>
            <input v-model="form.playDomain" placeholder="https://cdn.example.com（可留空，播放时替换原地址域名）" />
          </label>

          <p class="sf-group full">图片增强<span class="muted">封面等图片地址，同样只影响展示、不改动入库数据</span></p>
          <label class="sf-item full">
            <span>图片解析 / 代理地址</span>
            <input v-model="form.picApi" placeholder="https://img.example.com/proxy?url=（可留空，拼在图片地址前，支持 {url}）" />
          </label>
          <label class="sf-item full">
            <span>图片域名</span>
            <input v-model="form.picDomain" placeholder="https://img.example.com（可留空，展示时替换原图片域名）" />
          </label>

          <label class="sf-item">
            <span>增量小时数</span>
            <input v-model.number="form.incrementalHours" type="number" min="0" />
          </label>
          <label class="sf-item">
            <span>最大页数（0 = 全局配置）</span>
            <input v-model.number="form.maxPages" type="number" min="0" />
          </label>
          <p class="sf-group full">定时增量采集<span class="muted">到点自动发起一次增量任务，站点停用或已有任务在跑时不触发</span></p>
          <label class="check full"><input v-model="form.scheduleEnabled" type="checkbox" /> 启用定时增量采集</label>
          <label class="sf-item full">
            <span>执行时间（5 段 cron：分 时 日 月 周）</span>
            <input v-model="form.scheduleCron" list="cron-presets" :disabled="!form.scheduleEnabled"
                   :required="form.scheduleEnabled" placeholder="例：0 3 * * *（每天 03:00）" />
            <datalist id="cron-presets">
              <option value="*/30 * * * *">每 30 分钟</option>
              <option value="0 * * * *">每小时整点</option>
              <option value="0 */6 * * *">每 6 小时</option>
              <option value="0 3 * * *">每天 03:00</option>
              <option value="0 3 * * 1-5">工作日 03:00</option>
            </datalist>
            <span class="hint">支持 <code>*</code>、<code>*/n</code>、<code>a-b</code>、逗号列表；停服期间错过的时刻不补跑，同一分钟只触发一次。</span>
          </label>
          <div class="cron-feedback full" :class="cronFeedback.state" aria-live="polite">
            <template v-if="cronFeedback.state === 'off'">
              <span class="cron-icon">○</span>未启用定时增量采集，表达式不会生效。
            </template>
            <template v-else-if="cronFeedback.state === 'empty'">
              <span class="cron-icon">○</span>填写 cron 表达式后这里会实时显示校验结果与含义；也可以点输入框选一个常用预设。
            </template>
            <template v-else-if="cronFeedback.state === 'invalid'">
              <span class="cron-icon">✕</span>表达式无效：{{ cronFeedback.error }}
            </template>
            <template v-else>
              <div><span class="cron-icon">✓</span>表达式有效 · {{ cronFeedback.meaning }}</div>
              <div v-if="cronNextText" class="cron-next">接下来 {{ cronFeedback.next.length }} 次：{{ cronNextText }}</div>
            </template>
          </div>

          <label class="check full"><input v-model="form.enabled" type="checkbox" /> 启用该站点</label>

          <p class="hint full">
            增量采集附加 <code>h=&lt;小时数&gt;</code> 并按 <code>vod_time</code> 游标去重；接口返回的<strong>全部标准字段</strong>结构化入库，站点自定义字段存入 <code>extra</code>，不丢字段。<br />
            地址改写统一<strong>先换域名、再拼解析/代理地址</strong>：解析地址含 <code>{url}</code> 时按占位符插入，未含时直接前缀拼接；图片改写还作用于 <code>#</code> 分隔的截图集合。
          </p>
          <div class="sf-actions full">
            <button type="submit">{{ editing ? '保存修改' : '添加站点' }}</button>
            <button type="button" class="ghost" @click="closeSourceForm">取消</button>
          </div>
          <p v-if="error" class="error full">{{ error }}</p>
        </form>
      </div>
    </div>

    <!-- 站点订阅弹窗（新增 / 编辑共用，仅管理员） -->
    <div v-if="subOpen && isAdmin" class="modal-mask" @click.self="closeSubForm">
      <div class="modal narrow">
        <div class="modal-head">
          <div>
            <h3>{{ subEditing ? '编辑站点订阅' : '新增站点订阅' }}</h3>
            <p class="muted">远程 JSON：站点数组、单条站点对象，或本系统导出的 <code>{ sources: [...] }</code></p>
          </div>
          <button class="ghost" @click="closeSubForm">关闭</button>
        </div>
        <form class="modal-body sf-form" @submit.prevent="saveSubscription">
          <label class="sf-item">
            <span>订阅名称 *</span>
            <input v-model="subForm.name" placeholder="例：公共站点列表" required />
          </label>
          <label class="sf-item">
            <span>刷新间隔（小时，0 = 仅手动）</span>
            <input v-model.number="subForm.intervalHours" type="number" min="0" />
          </label>
          <label class="sf-item full">
            <span>订阅地址 *</span>
            <input v-model="subForm.url" placeholder="https://example.com/assetspider-sources.json" required />
          </label>
          <label class="check full"><input v-model="subForm.enabled" type="checkbox" /> 启用该订阅（关闭后仍可手动刷新）</label>
          <p class="hint full">
            刷新按<strong>接口地址</strong>匹配已有站点：命中则更新配置、未命中则新增；订阅里已移除的站点只<strong>停用不删除</strong>，影片与采集记录都保留。<br />
            已有站点的启用状态以本地为准（不会被订阅重新打开）；刷新只同步配置，<strong>不触发采集</strong>，采集仍按站点自己的定时与开关执行。
          </p>
          <div class="sf-actions full">
            <button type="submit" :disabled="subBusy">{{ subBusy ? '保存中…' : (subEditing ? '保存修改' : '添加订阅') }}</button>
            <button type="button" class="ghost" @click="closeSubForm">取消</button>
          </div>
        </form>
      </div>
    </div>

    <!-- 修改密码 -->
    <div v-if="pwdModal" class="modal-mask" @click.self="pwdModal = false">
      <div class="modal sm">
        <div class="modal-head">
          <h3>修改密码</h3>
          <button class="ghost" @click="pwdModal = false">关闭</button>
        </div>
        <div class="modal-body">
          <form class="user-form" @submit.prevent="doChangePassword">
            <input v-model="pwdForm.oldPassword" type="password" placeholder="原密码" required />
            <input v-model="pwdForm.newPassword" type="password" placeholder="新密码（至少 6 位）" required />
            <button type="submit">确认修改</button>
          </form>
          <p class="hint">修改后该账号的其他登录会话会立即失效，当前会话会自动换发新令牌。</p>
        </div>
      </div>
    </div>
  </template>
</template>

<style src="./styles/app.css"></style>
