<script setup>
// 采集记录页面：共享状态由 App.vue 通过 provide('app') 提供，
// 解构出的仍是原始 ref / computed / 函数，模板写法与拆分前一致。
import { inject } from 'vue'
const {
  barWidth, formatTime, isIndeterminate, jobMessage, jobs, modeLabel,
  pauseTask, rerunTask, resumeTask, retryTask, short, stat,
  statusLabel, taskStatusText
} = inject('app')
</script>

<template>
      <section class="card">
        <div class="list-head"><h2>采集记录</h2><span class="muted">最近 50 条</span></div>
        <p v-if="!jobs.length" class="empty">暂无采集记录。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>站点</th><th class="hide-sm">模式</th><th>状态</th><th>页数</th><th>拉取</th><th>新增</th><th>更新</th><th>未变</th><th class="hide-sm">开始时间</th><th class="hide-sm">说明</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="j in jobs" :key="j.id">
                <td class="mono">
                  {{ j.id }}
                  <div v-if="j.retryOf" class="tiny">源自 #{{ j.retryOf }}</div>
                </td>
                <td class="host">{{ j.sourceName }}</td>
                <td class="hide-sm">{{ modeLabel(j.mode) }}</td>
                <td>
                  <span class="badge" :class="j.status">{{ j.live ? taskStatusText(j.live) : statusLabel(j.status) }}</span>
                  <div v-if="stat(j, 'attempt') > 1" class="tiny">尝试 {{ stat(j, 'attempt') }} 次</div>
                </td>
                <td class="mono">
                  <template v-if="j.live">
                    <div class="prog-track sm" :class="{ indet: isIndeterminate(j.live) }">
                      <i :style="{ width: barWidth(j.live) }"></i>
                    </div>
                    <div class="tiny">
                      {{ j.live.pageCount > 0 ? j.live.page + '/' + j.live.pageCount : j.live.pages }} 页
                      · {{ j.live.pageCount > 0 ? Math.floor(j.live.percent) + '%' : '进行中' }}
                    </div>
                  </template>
                  <template v-else>{{ j.pages }}</template>
                </td>
                <td class="mono">{{ stat(j, 'fetched') }}</td>
                <td class="mono up">{{ stat(j, 'new') }}</td>
                <td class="mono">{{ stat(j, 'updated') }}</td>
                <td class="mono muted">{{ stat(j, 'unchanged') }}</td>
                <td class="muted hide-sm">{{ formatTime(j.startedAt) }}</td>
                <td class="muted msg hide-sm" :title="jobMessage(j)">{{ short(jobMessage(j), 30) }}</td>
                <td class="ops">
                  <template v-if="j.status === 'running' || j.status === 'queued'">
                    <button class="mini danger" @click="pauseTask(j.id)">暂停</button>
                  </template>
                  <template v-else>
                    <button v-if="j.status === 'paused'" class="mini" @click="resumeTask(j.id)">继续</button>
                    <button v-if="j.status === 'failed'" class="mini" @click="retryTask(j.id)">重试</button>
                    <button class="mini ghost" @click="rerunTask(j.id)">重新运行</button>
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
</template>
