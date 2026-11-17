<script setup>
// 我的接口：为当前账号生成 TVBox / 影视仓专属配置地址。
// 共享状态与行为由 App.vue 通过 provide('app') 提供。
import { inject, onMounted } from 'vue'
const {
  apiKey, apiKeyBusy, tvboxConfigURL,
  loadApiKey, regenerateApiKey, revokeApiKey, copyText,
} = inject('app')

onMounted(loadApiKey)
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
  </section>
</template>
