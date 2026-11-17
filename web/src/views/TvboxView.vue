<script setup>
// 我的接口：为当前账号生成 TVBox / 影视仓专属配置地址，并选择接口里开放哪些站点。
// 共享状态与行为由 App.vue 通过 provide('app') 提供。
import { inject, onMounted, ref, watch } from 'vue'
const {
  apiKey, apiKeyBusy, tvboxConfigURL, tvboxSources, tvboxSourcesBusy,
  tvboxAvailableSites,
  loadApiKey, regenerateApiKey, revokeApiKey, copyText,
  loadTVBoxSources, saveTVBoxSources, loadSources,
} = inject('app')

// 编辑中的选择（与服务端值解耦，保存后才写回）。
const editMode = ref('all')
const editSelected = ref([])

function syncFromServer() {
  const v = tvboxSources.value || {}
  editMode.value = v.mode === 'selected' ? 'selected' : 'all'
  const available = new Set((tvboxAvailableSites.value || []).map(s => s.id))
  editSelected.value = Array.isArray(v.selected)
    ? v.selected.map(Number).filter(id => available.size === 0 || available.has(id))
    : []
}

watch(tvboxSources, syncFromServer, { immediate: true })
// 站点列表异步加载完成后，重新同步一次以补全可勾选范围。
watch(tvboxAvailableSites, syncFromServer)

function toggleSite(id, checked) {
  const next = new Set(editSelected.value.map(Number))
  if (checked) next.add(Number(id))
  else next.delete(Number(id))
  editSelected.value = [...next].sort((a, b) => a - b)
}

async function save() {
  await saveTVBoxSources(editMode.value, editMode.value === 'selected' ? editSelected.value : [])
}

function selectAll() {
  editSelected.value = (tvboxAvailableSites.value || []).map(s => s.id)
}
function clearAll() {
  editSelected.value = []
}

onMounted(() => {
  // 站点勾选列表来自 ctx.sources，这里兜底再加载一次，避免全局 refresh
  // 尚未完成 / 失败时页面出现"没有可选择的站点"的假空态。
  Promise.allSettled([loadApiKey(), loadTVBoxSources(), loadSources()])
})
</script>

<template>
  <section class="card tvbox-view">
    <div class="list-head">
      <h2>我的 TVBox 接口</h2>
      <span class="muted">每个账号一个专属地址，可见站点与登录时一致</span>
    </div>

    <p class="hint">
      在 TVBox / 影视仓的「配置地址」里填入下方 URL，即可观看你有权限的站点资源。
      这个地址本身就是鉴权凭据，<strong>不要分享给他人</strong>。
    </p>

    <div class="tvbox-block">
      <label class="tvbox-label">配置地址</label>
      <template v-if="apiKey.key">
        <div class="tvbox-url">
          <code>{{ tvboxConfigURL }}</code>
          <button class="ghost" @click="copyText(tvboxConfigURL, '配置地址')">复制</button>
        </div>
        <p class="muted tiny">密钥：{{ apiKey.key }}</p>
      </template>
      <p v-else class="empty">尚未生成接口密钥，点击下方按钮生成。</p>
    </div>

    <div class="actions tvbox-actions">
      <button :disabled="apiKeyBusy" @click="regenerateApiKey">
        {{ apiKey.key ? '重置密钥' : '生成密钥' }}
      </button>
      <button v-if="apiKey.key" class="ghost danger" :disabled="apiKeyBusy" @click="revokeApiKey">
        撤销密钥
      </button>
    </div>
    <p class="muted tiny">
      重置或撤销后，旧地址立即失效，已在盒子上配置的需要改成新地址。
      账号被管理员停用后地址同样失效。
    </p>

    <!-- 接口里开放哪些站点（仅自己生效，不放大登录时的可见范围） -->
    <div class="tvbox-block tvbox-sources">
      <label class="tvbox-label">开放站点</label>
      <p class="muted tiny">选择这个 TVBox 接口里列出哪些站点。仅对你自己生效，且只能从你有权限的站点里选。</p>

      <div class="tvbox-mode">
        <label>
          <input type="radio" value="all" v-model="editMode" />
          全部开放（推荐）
        </label>
        <label>
          <input type="radio" value="selected" v-model="editMode" />
          仅开放勾选的站点
        </label>
      </div>

      <template v-if="editMode === 'selected'">
        <div class="tvbox-site-tools">
          <button class="ghost tiny" type="button" @click="selectAll">全选</button>
          <button class="ghost tiny" type="button" @click="clearAll">清空</button>
          <span class="muted tiny">已选 {{ editSelected.length }} 个</span>
        </div>
        <div v-if="tvboxAvailableSites.length" class="tvbox-site-list">
          <label v-for="site in tvboxAvailableSites" :key="site.id" class="tvbox-site-item">
            <input
              type="checkbox"
              :checked="editSelected.includes(site.id)"
              @change="toggleSite(site.id, $event.target.checked)"
            />
            <span>{{ site.name }}</span>
          </label>
        </div>
        <p v-else class="empty tiny">你当前没有可选择的站点，请联系管理员分配。</p>
      </template>

      <div class="actions tvbox-sources-actions">
        <button
          :disabled="tvboxSourcesBusy || (editMode === 'selected' && editSelected.length === 0 && tvboxAvailableSites.length > 0)"
          @click="save"
        >
          {{ tvboxSourcesBusy ? '保存中…' : '保存站点选择' }}
        </button>
      </div>
      <p class="muted tiny">保存后，TVBox / 影视仓里需要重新加载一次配置地址才会更新站点列表。</p>
    </div>
  </section>
</template>
