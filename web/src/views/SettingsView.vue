<script setup>
// 系统设置页：采集运行时配置（含并发上限）可在线修改、即时生效并写回配置文件。
// 共享状态由 App.vue 通过 provide('app') 提供。
import { computed, inject } from 'vue'
import { validateField } from '../settings.js'
const {
  capacity, settings, settingsForm, settingsDirty, settingsSaving,
  saveSettings
} = inject('app')

const fields = computed(() => settings.value?.fields || [])
// 表单值变化时标记未保存；范围校验交给纯函数（settings.js，有单测覆盖）。
function markDirty() { settingsDirty.value = true }
function fieldInvalid(f) { return validateField(f, settingsForm.value[f.key]) }
const anyInvalid = computed(() => fields.value.some(f => fieldInvalid(f)))
const canSave = computed(() => settingsDirty.value && !anyInvalid.value && !settingsSaving.value)
</script>

<template>
      <section class="card">
        <div class="list-head">
          <h2>系统设置</h2>
          <span class="muted">采集运行时配置 · 保存后立即生效并写入配置文件</span>
        </div>
        <p v-if="!settings" class="empty">正在加载配置…</p>
        <template v-else>
          <div class="settings-grid">
            <label v-for="f in fields" :key="f.key" class="setting-item" :class="{ locked: f.readOnly }">
              <span class="setting-label">
                {{ f.label }}
                <span v-if="f.readOnly" class="badge off" title="由环境变量控制，页面只读">环境变量</span>
                <span v-else-if="f.key === 'maxConcurrent'" class="badge info">原任务页并发上限</span>
              </span>
              <span class="setting-control">
                <input v-if="f.unit === '字符串'"
                       v-model="settingsForm[f.key]" type="text"
                       :disabled="f.readOnly" @input="markDirty" />
                <input v-else
                       v-model.number="settingsForm[f.key]" type="number"
                       :min="f.min" :max="f.max || undefined"
                       :disabled="f.readOnly" @input="markDirty" />
                <span v-if="f.unit && f.unit !== '字符串'" class="setting-unit">{{ f.unit }}</span>
              </span>
              <span class="setting-help">{{ f.help }}</span>
              <span v-if="fieldInvalid(f)" class="setting-error">{{ fieldInvalid(f) }}</span>
            </label>
          </div>

          <div class="settings-actions">
            <button :disabled="!canSave" @click="saveSettings">
              {{ settingsSaving ? '保存中…' : '保存设置' }}
            </button>
            <span v-if="settingsDirty" class="muted tiny">有未保存的修改</span>
            <span v-else class="muted tiny">当前运行并发 {{ capacity.running }} / {{ capacity.maxConcurrent }} · 排队 {{ capacity.queued }}</span>
          </div>

          <details class="tip">
            <summary>启动项（只读，需改配置文件并重启）</summary>
            <div class="settings-static">
              <div><span>监听地址</span><b class="mono">{{ settings.static?.serverAddr }}</b></div>
              <div><span>数据库</span><b class="mono">{{ settings.static?.databaseDriver }} · {{ settings.static?.databasePath }}</b></div>
              <div><span>前端目录</span><b class="mono">{{ settings.static?.webDir }}</b></div>
              <div><span>会话有效期</span><b class="mono">{{ settings.static?.tokenTtlHours }} 小时</b></div>
              <div><span>密码哈希强度</span><b class="mono">{{ settings.static?.bcryptCost }}</b></div>
            </div>
          </details>
        </template>
      </section>
</template>
