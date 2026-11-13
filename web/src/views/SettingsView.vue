<script setup>
// 系统设置页：采集运行时配置（含并发上限）可在线修改、即时生效并写回配置文件。
// 共享状态由 App.vue 通过 provide('app') 提供。
import { computed, inject } from 'vue'
import { validateField } from '../settings.js'
const {
  capacity, settings, settingsForm, dbForm, settingsDirty, settingsSaving,
  saveSettings, dbTesting, dbTestResult, testDatabase
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
              <div><span>当前数据库</span><b class="mono">{{ settings.static?.databaseDriver }} · {{ settings.static?.databasePath }}</b></div>
              <div><span>前端目录</span><b class="mono">{{ settings.static?.webDir }}</b></div>
              <div><span>会话有效期</span><b class="mono">{{ settings.static?.tokenTtlHours }} 小时</b></div>
              <div><span>密码哈希强度</span><b class="mono">{{ settings.static?.bcryptCost }}</b></div>
            </div>
          </details>

          <section class="card" v-if="settings.database">
            <div class="list-head">
              <h2>数据库</h2>
              <span class="muted">后台可编辑 · 保存后写入配置文件，需重启服务生效（不热重载连接）</span>
            </div>
            <p v-if="settings.database.readOnly" class="setting-error">
              数据库配置由环境变量接管，页面只读；请在启动环境中修改后重启。
            </p>
            <div class="settings-grid">
              <label class="setting-item" :class="{ locked: settings.database.readOnly }">
                <span class="setting-label">数据库类型</span>
                <span class="setting-control">
                  <select v-model="dbForm.driver" :disabled="settings.database.readOnly" @change="markDirty">
                    <option value="sqlite">SQLite（文件）</option>
                    <option value="mysql">MySQL</option>
                  </select>
                </span>
                <span class="setting-help">SQLite 适合单机部署；MySQL 适合多实例或更高并发，需提前建库并开启 ngram 全文索引。</span>
              </label>

              <template v-if="dbForm.driver === 'sqlite'">
                <label class="setting-item" :class="{ locked: settings.database.readOnly }">
                  <span class="setting-label">数据库文件</span>
                  <span class="setting-control">
                    <input v-model="dbForm.path" type="text" :disabled="settings.database.readOnly" @input="markDirty" />
                  </span>
                  <span class="setting-help">SQLite 数据库文件路径，相对或绝对路径。</span>
                </label>
              </template>

              <template v-else>
                <label class="setting-item" :class="{ locked: settings.database.readOnly }">
                  <span class="setting-label">连接串 DSN</span>
                  <span class="setting-control">
                    <input v-model="dbForm.dsn" type="text"
                           placeholder="user:pass@tcp(host:3306)/dbname?charset=utf8mb4&parseTime=True&loc=Local"
                           :disabled="settings.database.readOnly" @input="markDirty" />
                  </span>
                  <span class="setting-help">
                    含账号密码的连接串。留空表示保留当前已配置的连接串；从其它类型切换为 MySQL 时必须填写。
                    <span v-if="settings.database.dsnConfigured" class="badge info">已配置连接串（不展示明文）</span>
                  </span>
                </label>
                <label class="setting-item">
                  <span class="setting-label">最大连接数</span>
                  <span class="setting-control">
                    <input v-model.number="dbForm.maxOpenConns" type="number" min="0" @input="markDirty" />
                    <span class="setting-unit">个</span>
                  </span>
                  <span class="setting-help">连接池上限，0 用默认 16。</span>
                </label>
                <label class="setting-item">
                  <span class="setting-label">空闲连接数</span>
                  <span class="setting-control">
                    <input v-model.number="dbForm.maxIdleConns" type="number" min="0" @input="markDirty" />
                    <span class="setting-unit">个</span>
                  </span>
                  <span class="setting-help">空闲连接上限，0 用默认（上限的一半）。</span>
                </label>
                <label class="setting-item">
                  <span class="setting-label">连接最长存活</span>
                  <span class="setting-control">
                    <input v-model.number="dbForm.connMaxLifetimeMin" type="number" min="0" @input="markDirty" />
                    <span class="setting-unit">分钟</span>
                  </span>
                  <span class="setting-help">连接最长存活时间，0 用默认 30。</span>
                </label>
              </template>

              <div class="settings-actions db-test">
                <button type="button" :disabled="dbTesting || settings.database.readOnly" @click="testDatabase">
                  {{ dbTesting ? '测试中…' : '测试连接' }}
                </button>
                <span v-if="dbTestResult && dbTestResult.ok" class="badge on">连接成功 · {{ dbTestResult.message }}</span>
                <span v-else-if="dbTestResult && !dbTestResult.ok" class="badge off">{{ dbTestResult.error }}</span>
                <span v-else class="muted tiny">保存前可先探测当前配置是否可达（不写盘、不影响运行中的连接）</span>
              </div>

              <p class="setting-help mono" v-if="settings.database.display">
                当前连接目标：{{ settings.database.display }}
              </p>
              <p class="setting-help">{{ settings.database.applyNote }}</p>
            </div>
          </section>
        </template>
      </section>
</template>
