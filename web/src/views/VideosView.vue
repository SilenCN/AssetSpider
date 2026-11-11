<script setup>
// 资源库页面：共享状态由 App.vue 通过 provide('app') 提供，
// 解构出的仍是原始 ref / computed / 函数，模板写法与拆分前一致。
import { inject } from 'vue'
const {
  areaGroups, catSelectLabel, clearAllFilters, episodeLabel, error, filterByTag,
  formatTime, hasActiveFilters, isAdmin, markPicFailed, metaLine, onFilterChange,
  openDetail, posterOf, scoreOf, searchVideos, sources, tagListOf,
  tagSelectLabel, tags, videoAreaId, videoCategory, videoEnd, videoLoading,
  videoLoadingMore, videoQuery, videoSentinel, videoSort, videoSource, videoTag,
  videoYear, videos, visibleAreas, visibleCategories, visibleTags, years
} = inject('app')
</script>

<template>
      <section class="card">
        <div class="res-toolbar">
          <div class="search-box">
            <input v-model="videoQuery" placeholder="搜索片名 / 别名 / 演员 / 导演" @keyup.enter="searchVideos" />
            <button @click="searchVideos">搜索</button>
          </div>
          <div class="filters">
            <select v-model="videoCategory" @change="onFilterChange('category', $event)" title="按分类筛选">
              <option value="">全部分类</option>
              <option v-for="c in visibleCategories" :key="c.id" :value="c.id">{{ catSelectLabel(c) }}</option>
            </select>
            <select v-model="videoTag" @change="onFilterChange('tag', $event)" title="按标签筛选">
              <option value="">全部标签</option>
              <option v-for="t in visibleTags" :key="t.id" :value="t.id">{{ tagSelectLabel(t) }}</option>
            </select>
            <select v-model="videoAreaId" @change="onFilterChange('area', $event)" title="按地域筛选">
              <option value="">全部地域</option>
              <template v-if="videoSource">
                <option v-for="a in visibleAreas" :key="a.id" :value="a.id">{{ a.name }}（{{ a.videoCount }}）</option>
              </template>
              <template v-else>
                <optgroup v-for="g in areaGroups" :key="g.sourceId" :label="g.name">
                  <option v-for="a in g.items" :key="a.id" :value="a.id">{{ a.name }}（{{ a.videoCount }}）</option>
                </optgroup>
              </template>
            </select>
            <select v-model="videoYear" @change="onFilterChange('year', $event)" title="按年份筛选">
              <option value="">全部年份</option>
              <option v-for="y in years" :key="y.year" :value="y.year">{{ y.year }}（{{ y.videoCount }}）</option>
            </select>
            <select v-model="videoSource" @change="onFilterChange('source', $event)" title="按站点筛选">
              <option value="">全部站点</option>
              <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}</option>
            </select>
            <select v-model="videoSort" @change="onFilterChange('sort', $event)" title="排序">
              <option value="id_desc">默认（ID 倒序）</option>
              <option value="time_desc">更新时间 ↓</option>
              <option value="time_asc">更新时间 ↑</option>
              <option value="score_desc">评分 ↓</option>
              <option value="score_asc">评分 ↑</option>
            </select>
            <button class="ghost btn-clear" :disabled="!hasActiveFilters"
              @click="clearAllFilters" title="清空搜索词与全部分类、标签、地域、年份、站点条件">清空筛选</button>
          </div>
        </div>
        <div class="res-body">
          <p v-if="!videos.length && !videoLoading" class="empty">{{ isAdmin ? '暂无采集数据，可到「站点配置」执行一次全量采集。' : '暂无可见的采集数据。' }}</p>
          <div v-if="videos.length || videoLoading" class="res-grid">
            <article v-for="v in videos" :key="v.id" class="res-card" @click="openDetail(v)">
              <div class="res-poster">
                <img v-if="posterOf(v)" :src="posterOf(v)" :alt="v.vodName" loading="lazy" @error="markPicFailed(v)" />
                <div v-else class="res-poster-ph">{{ (v.vodName || '#').slice(0, 1) }}</div>
                <span v-if="scoreOf(v)" class="res-score">{{ scoreOf(v) }}</span>
                <span v-if="episodeLabel(v) !== '-'" class="res-eps">{{ episodeLabel(v) }}</span>
              </div>
              <div class="res-info">
                <h4 :title="v.vodName">{{ v.vodName || v.vodId }}</h4>
                <p v-if="metaLine(v)" class="res-meta">{{ metaLine(v) }}</p>
                <div v-if="tagListOf(v).length" class="res-tags">
                  <span v-for="t in tagListOf(v).slice(0, 4)" :key="t" class="res-tag" :title="`按标签「${t}」筛选`" @click.stop="filterByTag(t, v.sourceId)">{{ t }}</span>
                </div>
                <p class="res-time">{{ v.vodTime || formatTime(v.firstSeenAt) }}</p>
              </div>
            </article>
          </div>
          <!-- 整表重载（搜索 / 切筛选）时的遮罩：固定在视口中央，旧卡片留在下面打底 -->
          <div v-if="videoLoading" class="res-mask">
            <div class="res-mask-inner">
              <span class="spinner"></span>
              <span class="res-mask-text">{{ videoQuery.trim() ? '搜索中…' : '加载中…' }}</span>
            </div>
          </div>
          <!-- 无限滚动哨兵：进入视口即加载下一页 -->
          <div ref="videoSentinel" class="res-more">
            <template v-if="videoLoadingMore">
              <span class="spinner"></span><span>加载更多…</span>
            </template>
            <span v-else-if="videos.length && videoEnd" class="res-more-end">已经到底了</span>
          </div>
        </div>
      </section>
</template>
