<script setup>
// 视频分类页面：共享状态由 App.vue 通过 provide('app') 提供，
// 解构出的仍是原始 ref / computed / 函数，模板写法与拆分前一致。
import { inject } from 'vue'
const {
  catKey, catTotal, catVideoCount, categoryForest, isCatCollapsed, showCategoryVideos, toggleAllCats,
  toggleCat
} = inject('app')
</script>

<template>
      <section class="card">
        <div class="list-head">
          <h2>视频分类</h2>
          <span class="muted">
            {{ categoryForest.length }} 个站点 · {{ catTotal }} 个分类（支持二级）
            <button class="mini ghost" @click="toggleAllCats">展开 / 收起</button>
          </span>
        </div>
        <details class="tip">
          <summary>分类归档规则</summary>
          <p class="hint">采集时按接口返回的 type_id / type_name 自动创建分类并关联影片，每个资源站各自一套；依据苹果CMS 的 type_pid 模型支持二级分类：视频的 <code>type_id_1</code> 即所属一级分类 ID，二级分类会挂到对应一级分类之下。<b>点分类名称</b>即可按该分类（含其子分类）筛选影片。</p>
        </details>
        <p v-if="!categoryForest.length" class="empty">还没有分类，执行一次采集后会自动生成。</p>
        <div v-else class="tree">
          <div v-for="site in categoryForest" :key="site.key" class="tree-site">
            <div class="tree-row site" @click="toggleCat(site.key)">
              <span class="twisty" :class="{ open: !isCatCollapsed(site.key) }">▶</span>
              <b class="tree-title">{{ site.name }}</b>
              <span class="muted">{{ site.catCount }} 个分类 · {{ site.videoCount }} 部影片</span>
            </div>
            <div v-show="!isCatCollapsed(site.key)" class="tree-kids">
              <div v-for="node in site.children" :key="node.key || node.id" class="tree-branch">
                <div class="tree-row" :class="{ group: node.virtual }">
                  <span
                    class="twisty"
                    :class="{ open: !isCatCollapsed(catKey(node)), 'no-kids': !(node.children && node.children.length) }"
                    @click.stop="node.children && node.children.length && toggleCat(catKey(node))"
                  >▶</span>
                  <span class="badge" :class="node.virtual ? 'queued' : (node.level === 1 ? 'on' : 'off')">{{ node.virtual ? '分组' : (node.level === 1 ? '一级' : '二级') }}</span>
                  <button v-if="!node.virtual" type="button" class="tree-title link"
                          :title="`按「${node.name}」筛选影片（含子分类）`" @click.stop="showCategoryVideos(node)">{{ node.name }}</button>
                  <b v-else class="tree-title">{{ node.name }}</b>
                  <span v-if="!node.virtual" class="muted hide-sm">站点ID {{ node.typeId || '-' }}</span>
                  <span v-if="node.virtual" class="muted">{{ node.hint }}</span>
                  <span class="muted">{{ catVideoCount(node) }} 部</span>
                  <span class="tree-gap"></span>
                </div>
                <div v-if="node.children && node.children.length" v-show="!isCatCollapsed(catKey(node))" class="tree-kids">
                  <div v-for="ch in node.children" :key="ch.id" class="tree-row leaf">
                    <span class="twisty no-kids"></span>
                    <span class="badge off">二级</span>
                    <button type="button" class="tree-title link"
                            :title="`按「${ch.name}」筛选影片`" @click.stop="showCategoryVideos(ch)">{{ ch.name }}</button>
                    <span class="muted hide-sm">站点ID {{ ch.typeId || '-' }}</span>
                    <span class="muted">{{ ch.videoCount }} 部</span>
                    <span class="tree-gap"></span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>
</template>
