<script setup>
// 影片详情「页面」：占据主内容区，不再是弹窗 / 遮罩。
// 布局参考主流视频站：左侧播放器 + 线路/选集，右侧海报与资料卡；
// 接口原始字段（标准字段 / 扩展字段）属于内部信息，只对管理员折叠展示。
import { computed, inject, ref, watch } from 'vue'
const {
  detail, onlyNonEmpty, groupedFields, extraPairs, playGroups,
  playerEl, activeGroup, activeEp, currentEpisodes, currentEpisode, currentUrl,
  detailSource, useFrame, playError,
  metaLine, episodeLabel, scoreOf, tagListOf,
  chooseGroup, chooseEp, closeDetail, filterByTag, isAdmin,
  onVideoError, onVideoLoaded,
} = inject('app')

// 标签跳转：先记住影片所属站点，再离开详情页回资源库筛选，
// 避免 closeDetail 清空 detail 后 detailSource 变成 null 而匹配到别的站点同名标签。
function onTagClick(t) {
  const sid = detailSource.value?.id
  closeDetail()
  filterByTag(t, sid)
}

// 海报：多张图依次回退；全部失败时用首字占位块，避免破图。
const posterFailed = ref(false)
const posterUrl = computed(() => {
  const v = detail.value
  if (!v || posterFailed.value) return ''
  return v.vodPic || v.vodPicThumb || v.vodPicSlide || ''
})
const posterChar = computed(() => (detail.value?.vodName || '影').trim().slice(0, 1))

// 简介：站点给的是 HTML（含 <p>/<br> 与实体），转成纯文本段落展示。
const synopsis = computed(() => {
  const raw = detail.value?.vodContent || detail.value?.vodBlurb || ''
  return raw
    .replace(/<br\s*\/?>/gi, '\n')
    .replace(/<\/(p|div|li)>/gi, '\n')
    .replace(/<[^>]+>/g, '')
    .replace(/&nbsp;/g, ' ')
    .replace(/&amp;/g, '&')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim()
})
const synOpen = ref(false)

// 播放封面：未开始播放时用海报遮住播放器（含播放按钮），一开播即隐藏，
// 避免 native <video> 在加载时显示黑屏/转圈。切换线路或集数时重新显示。
const playing = ref(false)
watch(currentUrl, () => { playing.value = false })
watch(detail, () => {
  playing.value = false
  posterFailed.value = false
  synOpen.value = false
})

// 资料卡：只展示用户关心的信息，空值不占位。
const facts = computed(() => {
  const v = detail.value || {}
  return [
    ['导演', v.vodDirector],
    ['编剧', v.vodWriter],
    ['主演', v.vodActor],
    ['语言', v.vodLang],
    ['时长', v.vodDuration],
    ['更新', v.vodTime],
  ].filter(([, val]) => String(val || '').trim() !== '')
})
</script>

<template>
  <section class="card detail-page">
    <div class="detail-head">
      <button class="ghost detail-back" @click="closeDetail">← 返回资源库</button>
      <div class="detail-title">
        <h2>
          {{ detail.vodName || detail.vodId }}
          <small v-if="detail.vodSub" class="detail-sub">{{ detail.vodSub }}</small>
        </h2>
        <p class="meta-line">
          <span v-if="scoreOf(detail)" class="score-pill">★ {{ scoreOf(detail) }}</span>
          <span v-if="metaLine(detail)">{{ metaLine(detail) }}</span>
          <span v-if="episodeLabel(detail) !== '-'" class="badge on">{{ episodeLabel(detail) }}</span>
        </p>
        <div v-if="tagListOf(detail).length" class="res-tags detail-tags">
          <span v-for="t in tagListOf(detail)" :key="t" class="res-tag"
                :title="`按标签「${t}」筛选`" @click="onTagClick(t)">{{ t }}</span>
        </div>
      </div>
    </div>

    <div class="watch-body">
      <div class="watch-main">
        <div class="player-wrap">
          <div class="player-stage">
            <video v-show="!useFrame && playGroups.length" ref="playerEl" class="player"
                   controls playsinline preload="metadata"
                   @play="playing = true" @error="onVideoError" @loadeddata="onVideoLoaded"></video>
            <div v-if="!useFrame && playGroups.length && posterUrl && !playing"
                 class="player-cover" aria-hidden="true">
              <img :src="posterUrl" :alt="detail.vodName || ''" @error="posterFailed = true" />
              <span class="play-badge">▶</span>
            </div>
            <iframe v-if="useFrame" :key="currentUrl" class="player-frame" :src="currentUrl"
                    allow="autoplay; fullscreen; encrypted-media" allowfullscreen
                    referrerpolicy="no-referrer"></iframe>
            <div v-if="!playGroups.length" class="player-placeholder">
              <span class="ph-icon">🎬</span>
              <p>该影片暂无可播放的线路</p>
            </div>
          </div>
          <p v-if="useFrame && currentUrl" class="hint">
            正在通过解析页面播放；若上方无响应，可
            <a :href="currentUrl" target="_blank" rel="noopener" class="ep-url">在新窗口打开</a>。
          </p>
          <p v-else-if="playGroups.length && !currentUrl" class="hint">
            当前线路暂无播放地址，试试切换其它线路。
          </p>
          <p v-if="playError" class="play-error">
            {{ playError }}
            <template v-if="currentUrl"> · <a :href="currentUrl" target="_blank" rel="noopener" class="ep-url">在新窗口打开</a></template>
          </p>
        </div>

        <template v-if="playGroups.length">
          <div class="group-tabs">
            <span class="seg-label">线路</span>
            <button
              v-for="(g, gi) in playGroups" :key="gi"
              class="line-tab" :class="{ active: gi === activeGroup }"
              :title="g.note || g.from || ''"
              @click="chooseGroup(gi)"
            >
              {{ '线路 ' + (gi + 1) }}
              <em>{{ g.episodes?.length || 0 }} 集</em>
            </button>
          </div>
          <div class="ep-block">
            <div class="ep-head">
              选集
              <span class="muted" v-if="currentEpisodes.length">共 {{ currentEpisodes.length }} 集</span>
            </div>
            <div class="ep-chips">
              <button
                v-for="(ep, ei) in currentEpisodes" :key="ei"
                class="ep-chip" :class="{ active: ei === activeEp }"
                :title="ep.name || ('第' + (ei + 1) + '集')"
                @click="chooseEp(ei)"
              >{{ ep.name || ('第' + (ei + 1) + '集') }}</button>
              <span v-if="!currentEpisodes.length" class="muted">该线路暂无剧集。</span>
            </div>
          </div>
        </template>
      </div>

      <aside class="watch-side">
        <div class="poster-box">
          <img v-if="posterUrl" :src="posterUrl" :alt="detail.vodName || ''" @error="posterFailed = true" />
          <div v-else class="poster-placeholder">{{ posterChar }}</div>
        </div>

        <dl v-if="facts.length" class="facts">
          <div v-for="[k, val] in facts" :key="k" class="fact">
            <dt>{{ k }}</dt>
            <dd :title="String(val)">{{ val }}</dd>
          </div>
        </dl>

        <div v-if="synopsis" class="synopsis">
          <h4>简介</h4>
          <p :class="{ open: synOpen }">{{ synopsis }}</p>
          <button v-if="synopsis.length > 90" class="mini ghost syn-toggle" @click="synOpen = !synOpen">
            {{ synOpen ? '收起' : '展开' }}
          </button>
        </div>
      </aside>
    </div>

    <!-- 接口原始字段：属于内部数据，仅管理员可见、默认折叠 -->
    <details v-if="isAdmin" class="raw-fields">
      <summary>接口原始字段（仅管理员可见）</summary>
      <div class="raw-inner">
        <div class="raw-bar">
          <strong>标准字段</strong>
          <label class="check"><input v-model="onlyNonEmpty" type="checkbox" /> 只看非空</label>
        </div>
        <section v-for="g in groupedFields" :key="g.key" class="field-group">
          <h4>{{ g.label }} <span class="muted">{{ g.items.length }} 项</span></h4>
          <div class="field-grid">
            <div v-for="f in g.items" :key="f.key" class="field-item" :class="{ long: f.long }">
              <div class="field-k"><code>{{ f.key }}</code> {{ f.desc }}</div>
              <div class="field-v" :title="String(detail[f.json] ?? '')">{{ detail[f.json] ?? '-' }}</div>
            </div>
          </div>
        </section>
        <p v-if="!groupedFields.length" class="empty">没有可显示的标准字段。</p>

        <div class="raw-bar"><strong>扩展字段</strong></div>
        <p v-if="!extraPairs.length" class="empty">该站点未返回扩展字段。</p>
        <table v-else class="kv-table">
          <tbody><tr v-for="[k, val] in extraPairs" :key="k"><th><code>{{ k }}</code></th><td>{{ val }}</td></tr></tbody>
        </table>
      </div>
    </details>
  </section>
</template>
