<script setup>
// 站点配置页面：共享状态由 App.vue 通过 provide('app') 提供，
// 解构出的仍是原始 ref / computed / 函数，模板写法与拆分前一致。
import { inject } from 'vue'
const {
  activeTaskOf, barWidth, capacity, clearSourceData, crawlAll,
  editSource, exportSources, fmtEta, formatTime, importFileEl, isAdmin,
  isIndeterminate, modeLabel, onImportFile, openSourceForm, pauseTask,
  pickImportFile, queuedCountOf, removeSource, resumeTask, short,
  sources, startTask, statusLabel, taskStatusText, tasks, toggleSource
} = inject('app')
</script>

<template>
      <section class="card">
        <div class="list-head">
          <h2>采集任务</h2>
          <span class="muted">
            运行 {{ capacity.running }} / {{ capacity.maxConcurrent }} · 排队 {{ capacity.queued }}
          </span>
        </div>
        <div class="actions">
          <button @click="crawlAll('full')">全量采集可见站点</button>
          <button class="ghost" @click="crawlAll('incremental')">增量采集可见站点</button>
        </div>
        <p v-if="!tasks.length" class="empty">当前没有排队或运行中的采集任务。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr><th class="hide-sm">任务</th><th>站点</th><th>模式</th><th>状态</th><th class="prog-col">实时进度</th><th title="拉取 / 新增 / 更新 / 未变">拉取/新增/更新/未变</th><th class="hide-sm">速率</th><th class="hide-sm">已运行</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="t in tasks" :key="t.jobId">
                <td class="mono hide-sm">#{{ t.jobId }}</td>
                <td class="host">{{ t.sourceName }}</td>
                <td>
                  {{ modeLabel(t.mode) }}
                  <div v-if="t.maxAttempts > 1" class="tiny">第 {{ t.attempt }}/{{ t.maxAttempts }} 次尝试</div>
                </td>
                <td>
                  <span class="badge" :class="t.retryInSeconds > 0 ? 'paused' : t.status">{{ taskStatusText(t) }}</span>
                </td>
                <td class="prog-col">
                  <div class="prog-track" :class="{ indet: isIndeterminate(t) }">
                    <i :style="{ width: barWidth(t) }"></i>
                  </div>
                  <div class="prog-meta">
                    <b>{{ isIndeterminate(t) ? '—' : Math.floor(t.percent) + '%' }}</b>
                    <span v-if="t.pageCount > 0">{{ t.page }} / {{ t.pageCount }} 页</span>
                    <span v-else>{{ t.pages }} 页</span>
                    <span>{{ t.fetched }} 条</span>
                    <span v-if="t.etaSeconds > 0">剩余 {{ fmtEta(t.etaSeconds) }}</span>
                  </div>
                  <div v-if="t.message" class="tiny msg" :title="t.message">{{ short(t.message, 60) }}</div>
                </td>
                <td class="mono stats" :title="`拉取 ${t.fetched} 条，新增 ${t.new}，更新 ${t.updated}，未变 ${t.unchanged}`">
                  {{ t.fetched }} / <span class="up">{{ t.new }}</span> / {{ t.updated }} / <span class="muted">{{ t.unchanged }}</span>
                </td>
                <td class="mono muted hide-sm">{{ t.itemsPerSecond > 0 ? t.itemsPerSecond.toFixed(1) + ' 条/秒' : '-' }}</td>
                <td class="muted hide-sm">{{ t.elapsedSeconds }}s</td>
                <td class="ops">
                  <button v-if="t.status === 'running' || t.status === 'queued'"
                          class="mini danger" @click="pauseTask(t.jobId)">暂停</button>
                  <button v-else-if="t.status === 'paused'"
                          class="mini ghost" @click="resumeTask(t.jobId)">继续</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <div class="list-head">
          <div class="list-head-l">
            <h2>站点列表</h2><span class="muted">共 {{ sources.length }} 个</span>
          </div>
          <div class="ops import-ops">
            <button class="ghost" @click="exportSources">导出 JSON</button>
            <button v-if="isAdmin" class="ghost" @click="pickImportFile">导入 JSON</button>
            <button v-if="isAdmin" @click="openSourceForm()">+ 新增站点</button>
            <input ref="importFileEl" class="hidden-file" type="file" accept=".json,application/json" @change="onImportFile" />
          </div>
        </div>
        <p v-if="!sources.length" class="empty">{{ isAdmin ? '还没有站点，点右上角「新增站点」添加一个 JSON 采集接口。' : '管理员尚未给你分配任何可见站点。' }}</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th class="hide-sm">ID</th><th>名称</th><th class="hide-sm">接口地址</th><th>状态</th><th>影片数</th>
                <th class="hide-sm">上次采集</th><th class="hide-sm">游标 / 结果</th><th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in sources" :key="s.id">
                <td class="mono hide-sm">{{ s.id }}</td>
                <td class="host">
                  {{ s.name }}
                  <div v-if="s.scheduleEnabled" class="tiny muted"
                       :title="`定时增量：${s.scheduleCron}｜下次 ${formatTime(s.nextScheduleAt)}｜上次触发 ${formatTime(s.scheduleLastAt)}`">
                    ⏰ {{ s.scheduleCron }} · 下次 {{ formatTime(s.nextScheduleAt) }}
                    <span v-if="!s.enabled">（站点停用，不触发）</span>
                  </div>
                  <div v-if="s.parseApi || s.playDomain || s.picApi || s.picDomain" class="tiny muted" :title="[s.parseApi && ('解析：' + s.parseApi), s.playDomain && ('播放域名：' + s.playDomain), s.picApi && ('图片代理：' + s.picApi), s.picDomain && ('图片域名：' + s.picDomain)].filter(Boolean).join(' / ')">
                    {{ [s.parseApi && '解析播放', s.playDomain && '播放域名替换', (s.picApi || s.picDomain) && '图片改写'].filter(Boolean).join(' · ') }}
                  </div>
                </td>
                <td class="url hide-sm" :title="s.apiBase">{{ short(s.apiBase, 38) }}</td>
                <td>
                  <span class="badge" :class="s.enabled ? 'on' : 'off'">{{ s.enabled ? '启用' : '停用' }}</span>
                </td>
                <td class="mono" title="已入库影片总数：采集运行中按进度落库间隔（默认每分钟）刷新，任务结束时再刷新一次">{{ s.videoCount }}</td>
                <td class="muted hide-sm">{{ formatTime(s.lastSyncAt) }}</td>
                <td class="muted hide-sm">
                  <span class="badge" :class="s.lastStatus">{{ statusLabel(s.lastStatus) }}</span>
                  <div class="mono tiny">{{ formatTime(s.lastCursor) }}</div>
                </td>
                <td class="ops">
                  <template v-if="activeTaskOf(s.id)">
                    <span class="muted tiny">
                      {{ statusLabel(activeTaskOf(s.id).status) }}…
                      <template v-if="activeTaskOf(s.id).status === 'running' && queuedCountOf(s.id)">
                        （另有 {{ queuedCountOf(s.id) }} 个排队）
                      </template>
                    </span>
                    <button class="mini danger" @click="pauseTask(activeTaskOf(s.id).jobId)">暂停</button>
                  </template>
                  <template v-else>
                    <button class="mini" @click="startTask(s, 'full')">全量</button>
                    <button class="mini ghost" @click="startTask(s, 'incremental')">增量</button>
                  </template>
                  <template v-if="isAdmin">
                    <button class="mini ghost" @click="editSource(s)">编辑</button>
                    <button class="mini ghost" @click="toggleSource(s)">{{ s.enabled ? '停用' : '启用' }}</button>
                    <button class="mini ghost" @click="clearSourceData(s)" title="删除该站点的影片、分类、标签与采集记录，保留站点配置">清空</button>
                    <button class="mini danger" @click="removeSource(s)">删除</button>
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
</template>
