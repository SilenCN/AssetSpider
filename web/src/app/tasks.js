// 采集任务：入队、批量采集、暂停 / 继续 / 重试 / 重跑，以及站点上的活动任务查询。
export function createTasks(ctx) {
  async function loadJobs() {
    ctx.jobs.value = (await ctx.request('/api/jobs/v1?limit=50')) || []
  }

  // 采集任务（排队/运行/暂停）与并发容量
  async function loadTasks() {
    const d = (await ctx.request('/api/tasks/v1')) || {}
    ctx.tasks.value = d.items || []
    if (d.capacity) ctx.capacity.value = d.capacity
  }

  // 站点正在进行的采集任务（排队/运行），用于把「全量/增量」换成「暂停」。
  // 同一站点现在可以「1 个运行 + N 个排队」，这里优先返回真正在跑的那个，
  // 让暂停操作作用在当前任务上。
  function activeTaskOf(sourceId) {
    const active = ctx.tasks.value.filter(t =>
      t.sourceId === sourceId && (t.status === 'running' || t.status === 'queued'))
    return active.find(t => t.status === 'running') || active[0] || null
  }
  // 该站点排队中的任务数（同站点串行，多出来的都在等）
  function queuedCountOf(sourceId) {
    return ctx.tasks.value.filter(t => t.sourceId === sourceId && t.status === 'queued').length
  }

  // 采集改为异步任务：发起后立即返回，进度在「采集任务」里实时轮询；
  // 运行中的任务可随时手动暂停（已完成页的数据已入库，续采页码会保存）。
  async function startTask(s, mode) {
    const label = ctx.modeLabel(mode)
    try {
      const t = await ctx.request('/api/tasks/v1', ctx.jsonOptions('POST', { sourceId: s.id, mode }))
      ctx.notify(`「${s.name}」${label}采集已入队（任务 #${t.jobId}）`, 'info')
      await loadTasks()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  async function crawlAll(mode) {
    const list = ctx.sources.value.filter(s => s.enabled)
    if (!list.length) { ctx.notify('没有启用的站点', 'err'); return }
    const label = ctx.modeLabel(mode)
    let ok = 0
    const failures = []
    for (const s of list) {
      try {
        await ctx.request('/api/tasks/v1', ctx.jsonOptions('POST', { sourceId: s.id, mode }))
        ok++
      } catch (e) { failures.push(`${s.name}: ${e.message}`) }
    }
    if (ok) {
      ctx.notify(`已提交 ${ok}/${list.length} 个${label}任务，超出并发上限的会在队列中等待`, failures.length ? 'err' : 'ok')
    } else {
      ctx.notify(`批量采集失败：${failures[0] || '未知错误'}`, 'err')
    }
    await loadTasks()
  }

  async function pauseTask(jobId) {
    try {
      await ctx.request(`/api/tasks/v1/${jobId}`, ctx.jsonOptions('PUT', { status: 'paused' }))
      ctx.notify(`任务 #${jobId} 已暂停，进度已保存`, 'ok')
      await Promise.all([loadTasks(), loadJobs()])
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  async function resumeTask(jobId) {
    try {
      const t = await ctx.request(`/api/tasks/v1/${jobId}`, ctx.jsonOptions('PUT', { status: 'running' }))
      ctx.notify(`任务 #${t.jobId} 已继续，从暂停页接着采（同一记录，统计累加）`, 'ok')
      await Promise.all([loadTasks(), loadJobs()])
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  // 失败任务：复用原记录，从失败页续采（统计与尝试次数继续累加）
  async function retryTask(jobId) {
    try {
      const t = await ctx.request(`/api/tasks/v1/${jobId}/retry`, { method: 'POST' })
      ctx.notify(`任务 #${t.jobId} 已重新入队，从失败页继续（同一记录，统计累加）`, 'ok')
      await Promise.all([loadTasks(), loadJobs()])
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  // 已结束任务：复用原记录，忽略断点从第 1 页重采，统计清零
  async function rerunTask(jobId) {
    try {
      const t = await ctx.request(`/api/tasks/v1/${jobId}/rerun`, { method: 'POST' })
      ctx.notify(`任务 #${t.jobId} 已重新运行，从第 1 页开始（同一记录，统计已清零）`, 'ok')
      await Promise.all([loadTasks(), loadJobs()])
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  return {
    loadJobs, loadTasks, activeTaskOf, queuedCountOf,
    startTask, crawlAll, pauseTask, resumeTask, retryTask, rerunTask,
  }
}
