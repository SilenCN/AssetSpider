<script setup>
// 影片详情「页面」：占据主内容区，不再是弹窗 / 遮罩。
// 共享状态由 App.vue 通过 provide('app') 提供，解构出的仍是原始 ref / computed / 函数，
// 因此模板 ref（播放器）、v-model 与拆分前一致。
import { inject } from 'vue'
const {
  detail, detailTab, onlyNonEmpty, groupedFields, extraPairs, playGroups,
  playerEl, activeGroup, activeEp, currentEpisodes, currentEpisode, currentUrl,
  detailSource, parseEnabled, useFrame, playError,
  metaLine, episodeLabel, scoreOf, tagListOf,
  switchDetailTab, chooseGroup, chooseEp, closeDetail, filterByTag,
  onVideoError, onVideoLoaded,
} = inject('app')

// 标签跳转：先记住影片所属站点，再离开详情页回资源库筛选，
// 避免 closeDetail 清空 detail 后 detailSource 变成 null 而匹配到别的站点同名标签。
function onTagClick(t) {
  const sid = detailSource.value?.id
  closeDetail()
  filterByTag(t, sid)
}
</script>

<template>
  <section class="card detail-page">
    <div class="detail-head">
      <button class="ghost detail-back" @click="closeDetail">← 返回资源库</button>
      <div class="detail-title">
        <h2>{{ detail.vodName || detail.vodId }}</h2>
        <p class="muted">
          <span v-if="metaLine(detail)">{{ metaLine(detail) }} · </span>
          更新：{{ episodeLabel(detail) }} · {{ detail.vodTime || '-' }}
          <span v-if="scoreOf(detail)"> · 评分 {{ scoreOf(detail) }}</span>
          <span v-if="detail.vodRemarks && detail.vodRemarks !== episodeLabel(detail)"> · {{ detail.vodRemarks }}</span>
        </p>
        <div v-if="tagListOf(detail).length" class="res-tags detail-tags">
          <span v-for="t in tagListOf(detail)" :key="t" class="res-tag"
                :title="`按标签「${t}」筛选`" @click="onTagClick(t)">{{ t }}</span>
        </div>
      </div>
    </div>

    <div class="detail-tabs">
      <button :class="{ active: detailTab === 'play' }" @click="switchDetailTab('play')">播放组 ({{ playGroups.length }})</button>
      <button :class="{ active: detailTab === 'fields' }" @click="switchDetailTab('fields')">标准字段</button>
      <button :class="{ active: detailTab === 'extra' }" @click="switchDetailTab('extra')">扩展字段 ({{ extraPairs.length }})</button>
      <label v-if="detailTab === 'fields'" class="check"><input v-model="onlyNonEmpty" type="checkbox" /> 只看非空</label>
    </div>

    <div class="detail-body">
      <template v-if="detailTab === 'play'">
        <div class="player-wrap">
          <video v-show="!useFrame && playGroups.length" ref="playerEl" class="player" controls playsinline preload="metadata"
                 @error="onVideoError" @loadeddata="onVideoLoaded"></video>
          <iframe v-if="useFrame" :key="currentUrl" class="player-frame" :src="currentUrl"
                  allow="autoplay; fullscreen; encrypted-media" allowfullscreen
                  referrerpolicy="no-referrer"></iframe>
          <p v-if="!playGroups.length" class="empty">该影片没有播放地址（vod_play_url 为空）。</p>
          <p v-else-if="useFrame" class="hint">
            已按站点配置拼接<strong>视频解析地址</strong>，下面用解析页面播放；若解析站要求手动播放，
            可在<strong>解析页面内</strong>点击播放。
            <a v-if="currentUrl" :href="currentUrl" target="_blank" rel="noopener" class="ep-url">在新窗口打开</a>
          </p>
          <p v-else-if="!currentUrl" class="hint">
            当前线路这一集没有播放地址。
          </p>
          <p v-else class="hint">m3u8 优先用 hls.js 播放，mp4 / webm 走直链；无后缀地址会先按 HLS 试播再回退直链。切换线路或集数即时切换。</p>
          <p v-if="playError" class="play-error">
            {{ playError }}
            <a v-if="currentUrl" :href="currentUrl" target="_blank" rel="noopener" class="ep-url">在新窗口打开</a>
          </p>
        </div>
        <template v-if="playGroups.length">
          <div class="group-tabs">
            <button
              v-for="(g, gi) in playGroups" :key="gi"
              class="mini" :class="gi === activeGroup ? '' : 'ghost'"
              :title="g.note || ''"
              @click="chooseGroup(gi)"
            >{{ g.from || ('线路' + (gi + 1)) }} ({{ g.episodes?.length || 0 }})</button>
          </div>
          <div class="ep-chips">
            <button
              v-for="(ep, ei) in currentEpisodes" :key="ei"
              class="mini" :class="ei === activeEp ? '' : 'ghost'"
              @click="chooseEp(ei)"
            >{{ ep.name || ('第' + (ei + 1) + '集') }}</button>
            <span v-if="!currentEpisodes.length" class="muted">该线路暂无剧集。</span>
          </div>
          <p v-if="currentEpisode?.url" class="play-url">
            <span v-if="detailSource?.playDomain" class="badge on">播放域名已替换</span>
            <span v-if="parseEnabled" class="badge on">已拼接解析地址</span>
            {{ currentEpisode.url }}
          </p>
        </template>
      </template>
      <template v-else-if="detailTab === 'fields'">
        <section v-for="g in groupedFields" :key="g.key" class="field-group">
          <h4>{{ g.label }} <span class="muted">{{ g.items.length }} 项</span></h4>
          <div class="field-grid">
            <div v-for="f in g.items" :key="f.key" class="field-item" :class="{ long: f.long }">
              <div class="field-k"><code>{{ f.key }}</code> {{ f.desc }}</div>
              <div class="field-v" :title="String(detail[f.json] ?? '')">{{ detail[f.json] ?? '-' }}</div>
            </div>
          </div>
        </section>
        <p v-if="!groupedFields.length" class="empty">没有可显示的字段。</p>
      </template>
      <template v-else-if="detailTab === 'extra'">
        <p class="hint">接口返回但不在标准字段清单内的字段（站点自定义字段、嵌套对象、数组等），按原始 JSON 值保存，确保不丢失。</p>
        <p v-if="!extraPairs.length" class="empty">该站点未返回扩展字段。</p>
        <table v-else class="kv-table">
          <tbody><tr v-for="[k, val] in extraPairs" :key="k"><th><code>{{ k }}</code></th><td>{{ val }}</td></tr></tbody>
        </table>
      </template>
    </div>
  </section>
</template>
