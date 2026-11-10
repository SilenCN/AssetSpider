<script setup>
// 用户管理页面：共享状态由 App.vue 通过 provide('app') 提供，
// 解构出的仍是原始 ref / computed / 函数，模板写法与拆分前一致。
import { inject } from 'vue'
const {
  editUser, form, formatTime, me, removeUser, resetUserForm,
  saveUser, sources, toggleUser, userEditing, userForm, users,
  visibleLabel
} = inject('app')
</script>

<template>
      <section class="card">
        <h2>{{ userEditing ? `编辑用户「${userForm.username}」` : '新增用户' }}</h2>
        <form class="user-form" @submit.prevent="saveUser">
          <input v-model="userForm.username" placeholder="用户名 *" :disabled="userEditing" :required="!userEditing" />
          <input v-model="userForm.password" type="password"
                 :placeholder="userEditing ? '新密码（留空不改）' : '密码 *（至少 6 位）'" :required="!userEditing" />
          <label class="field">角色
            <select v-model="userForm.role">
              <option value="user">普通用户</option>
              <option value="admin">管理员</option>
            </select>
          </label>
          <label class="check"><input v-model="userForm.enabled" type="checkbox" /> 启用</label>
          <button type="submit">{{ userEditing ? '保存修改' : '创建用户' }}</button>
          <button v-if="userEditing" type="button" class="ghost" @click="resetUserForm">取消</button>
          <div class="source-grants">
            <span class="muted">可见站点授权</span>
            <span class="hint">普通用户只能看见被勾选的站点；管理员默认可见全部，无需授权。</span>
            <div class="grant-list">
              <label v-for="s in sources" :key="s.id" class="check">
                <input type="checkbox" :value="s.id" v-model="userForm.sourceIds" /> {{ s.name }}
              </label>
              <span v-if="!sources.length" class="muted">还没有站点可分配</span>
            </div>
          </div>
        </form>
        <p class="hint">创建/修改后立即生效，无需用户重新登录。停用、改密或变更角色会使该用户的现有会话失效。</p>
      </section>

      <section class="card">
        <div class="list-head"><h2>用户列表</h2><span class="muted">共 {{ users.length }} 个账号</span></div>
        <p v-if="!users.length" class="empty">还没有其他账号。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>用户名</th><th>角色</th><th>状态</th><th>可见站点</th><th class="hide-sm">最后登录</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="u in users" :key="u.id">
                <td class="mono">{{ u.id }}</td>
                <td class="host">{{ u.username }}<span v-if="u.id === me.user?.id" class="tiny muted">（我）</span></td>
                <td><span class="badge" :class="u.role === 'admin' ? 'on' : 'off'">{{ u.role === 'admin' ? '管理员' : '普通用户' }}</span></td>
                <td><span class="badge" :class="u.enabled ? 'on' : 'off'">{{ u.enabled ? '启用' : '停用' }}</span></td>
                <td class="muted">{{ visibleLabel(u) }}</td>
                <td class="muted hide-sm">{{ formatTime(u.lastLoginAt) }}</td>
                <td class="ops">
                  <button class="mini ghost" @click="editUser(u)">编辑</button>
                  <button class="mini ghost" @click="toggleUser(u)">{{ u.enabled ? '停用' : '启用' }}</button>
                  <button class="mini danger" @click="removeUser(u)">删除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
</template>
