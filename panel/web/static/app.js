'use strict';
// 所有内容都用 textContent 构建，不使用 innerHTML，服务器返回的数据不会被当成 HTML 解析。

const BASE = document.querySelector('meta[name=base]').content;
const $ = (id) => document.getElementById(id);

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

async function api(path, opts) {
  const r = await fetch(BASE + path, Object.assign({ credentials: 'same-origin' }, opts));
  if (r.status === 401) { location.href = BASE + 'login'; throw new Error('未登录'); }
  let data = null;
  try { data = await r.json(); } catch (_) { /* 非 JSON 响应 */ }
  return { ok: r.ok, status: r.status, data };
}

const post = (path, body) => api(path, {
  method: 'POST',
  headers: body instanceof URLSearchParams ? {} : { 'Content-Type': 'application/json' },
  body: body instanceof URLSearchParams ? body : JSON.stringify(body || {}),
});

// ---------- 复制 ----------
function copyText(text, btn) {
  const done = () => { const t = btn.textContent; btn.textContent = '已复制'; setTimeout(() => { btn.textContent = t; }, 1200); };
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(done, () => fallbackCopy(text, done));
  } else {
    fallbackCopy(text, done); // HTTP 入口下没有 clipboard API
  }
}
function fallbackCopy(text, done) {
  const ta = h('textarea', { readonly: '' });
  ta.value = text;
  document.body.append(ta);
  ta.select();
  try { document.execCommand('copy'); done(); } catch (_) { /* 复制失败就算了 */ }
  ta.remove();
}

function linkRow(label, url, current) {
  const btn = h('button', {}, '复制');
  btn.addEventListener('click', () => copyText(url, btn));
  return h('div', { class: 'link' },
    h('div', { class: 'url', title: label }, url),
    current ? h('span', { class: 'here' }, '当前') : null,
    btn);
}

// ---------- 标签页 ----------
let currentTab = 'overview';
function showTab(name) {
  currentTab = name;
  for (const b of document.querySelectorAll('#tabs button')) b.classList.toggle('active', b.dataset.tab === name);
  for (const s of document.querySelectorAll('main > section')) s.hidden = s.id !== 'tab-' + name;
  if (name === 'overview') loadOverview();
  if (name === 'rules') loadRules();
  if (name === 'logs') loadLogs();
}
$('tabs').addEventListener('click', (e) => { if (e.target.dataset.tab) showTab(e.target.dataset.tab); });

// ---------- 概览 ----------
let lastPanel = [];

async function loadOverview() {
  const { ok, data } = await api('api/overview');
  if (!ok) return;
  lastPanel = data.panel || [];
  const here = location.host;
  const root = $('tab-overview');
  root.replaceChildren(
    h('div', { class: 'grid' },
      h('div', { class: 'card' }, h('h2', {}, '服务状态'),
        data.services.map((s) => h('div', { class: 'kv' }, h('span', {}, s.name),
          h('span', { class: 'pill ' + (s.active ? 'ok' : 'bad') }, s.active ? '运行中' : s.state)))),
      h('div', { class: 'card' }, h('h2', {}, '基本信息'),
        h('div', { class: 'kv' }, h('span', {}, '内核'), h('span', {}, data.kernel || '未知')),
        h('div', { class: 'kv' }, h('span', {}, '服务器地址'), h('span', {}, data.server_ip)),
        h('div', { class: 'kv' }, h('span', {}, '伪装域名 SNI'), h('span', {}, data.sni)),
        h('div', { class: 'kv' }, h('span', {}, 'Argo 域名'), h('span', {}, data.argo_domain || '（暂无）')),
        h('div', { class: 'kv' }, h('span', {}, '服务器时间'), h('span', {}, data.now))),
      h('div', { class: 'card' }, h('h2', {}, '端口'),
        data.ports.map((p) => h('div', { class: 'kv' }, h('span', {}, p.name),
          h('span', {}, p.value, p.note ? h('span', { class: 'muted' }, '  ' + p.note) : null))))),
    h('div', { class: 'card' }, h('h2', {}, '面板入口'),
      data.panel.map((p) => h('div', {}, h('div', { class: 'muted' }, p.name + (p.note ? '（' + p.note + '）' : '')),
        linkRow(p.name, p.value, new URL(p.value).host === here)))),
    h('div', { class: 'card' }, h('h2', {}, '订阅地址'),
      (data.subs || []).map((s) => h('div', {}, h('div', { class: 'muted' }, s.name), linkRow(s.name, s.value)))),
    h('div', { class: 'card' }, h('h2', {}, '节点链接'),
      h('p', { class: 'muted' }, 'ShadowTLS 没有通用链接格式，请用 Clash 订阅。'),
      (data.nodes || []).map((n) => linkRow('节点', n))));
}
setInterval(() => { if (currentTab === 'overview' && $('job').hidden) loadOverview(); }, 15000);

// ---------- 任务 ----------
const ACTIONS = [
  { name: 'set-sni', title: '更换 SNI', desc: 'Reality / ShadowTLS 伪装用的域名，并重新生成订阅。', host: true, ok: '更换后需要在客户端重新拉取订阅。' },
  { name: 'reroll-ports', title: '随机所有端口', desc: '重新随机各节点端口和订阅端口。', ok: '订阅地址和面板 HTTP 入口都会变化，所有客户端要重新导入订阅。' },
  { name: 'reset-keys', title: '重置密钥', desc: '重新生成 UUID、Reality 密钥和各类密码。', ok: '所有旧客户端立即失效，需要重新导入订阅。' },
  { name: 'reset-token', title: '更换订阅地址', desc: '更换订阅地址里的随机 token。', ok: '旧订阅地址立即失效；vmess-ws 约 60 秒内不可用。' },
  { name: 'refresh-argo', title: '刷新 Argo 域名', desc: '重启 Argo 隧道并重新同步临时域名。', ok: 'vmess-ws 约 60 秒内不可用。' },
  { name: 'restart', title: '重启服务', desc: '重启 sing-box、sb-nginx、sb-argo。', ok: '连接会短暂中断。' },
  { name: 'update', title: '更新 sing-box 内核', desc: '下载最新版 sing-box 并重启。', ok: '连接会短暂中断。' },
  { name: 'bbr', title: '开启 BBR', desc: '启用 BBR 拥塞控制（需要内核 4.9+）。', ok: '' },
];
const ARGO_WARN = '\n\n⚠ 这个操作会重启 Argo 隧道，临时域名会变化。如果你现在是通过 https://xxx.trycloudflare.com 访问本页，操作完成后这个地址会失效，页面会失去连接。请改用 HTTP 入口，或在服务器上执行 vps panel 查看新地址。';
const ARGO_ACTIONS = new Set(['set-sni', 'reroll-ports', 'reset-keys', 'reset-token', 'refresh-argo', 'restart', 'update']);

function buildOps() {
  const hostInput = h('input', { placeholder: '例如 www.microsoft.com', size: '28' });
  const rows = ACTIONS.map((a) => {
    const btn = h('button', { class: a.name === 'reset-keys' ? 'danger' : '' }, a.title);
    btn.addEventListener('click', () => {
      const body = new URLSearchParams();
      if (a.host) {
        const v = hostInput.value.trim();
        if (!v) { alert('请先填写域名'); return; }
        body.set('host', v);
      }
      let msg = '确认执行「' + a.title + '」？' + (a.ok ? '\n\n' + a.ok : '');
      if (ARGO_ACTIONS.has(a.name)) msg += ARGO_WARN;
      if (confirm(msg)) runAction(a.name, body);
    });
    return h('div', { class: 'op' },
      h('div', {}, h('strong', {}, a.title), h('p', { class: 'muted' }, a.desc), a.host ? h('div', { class: 'row' }, hostInput) : null),
      btn);
  });
  $('tab-ops').replaceChildren(h('div', { class: 'card' }, h('h2', {}, '运维操作'),
    h('p', { class: 'muted' }, '同一时间只能执行一个任务；结果显示在页面底部。避免同时在 SSH 里操作，以免互相覆盖。'), rows));
}

let polling = false;

function showJob(j) {
  $('job').hidden = false;
  const st = $('job-state');
  st.textContent = j.state === 'running' ? '运行中' : j.state === 'ok' ? '成功' : '失败（退出码 ' + j.exit_code + '）';
  st.className = 'pill ' + (j.state === 'running' ? 'run' : j.state === 'ok' ? 'ok' : 'bad');
  const out = $('job-out');
  const atBottom = out.scrollTop + out.clientHeight >= out.scrollHeight - 20;
  out.textContent = j.output;
  if (atBottom) out.scrollTop = out.scrollHeight;
}

async function runAction(name, body) {
  const { ok, status, data } = await post('api/action/' + name, body);
  if (!ok) { alert((data && data.error) || '请求失败（' + status + '）'); return; }
  watchJob(data.restarts_argo);
}

// 轮询任务；如果连接断了（常见于经 Argo 访问时隧道被重启），给出明确的提示
async function watchJob(restartsArgo) {
  if (polling) return;
  polling = true;
  let fails = 0;
  const before = lastPanel.slice();
  for (;;) {
    await new Promise((r) => setTimeout(r, 1500));
    let res;
    try { res = await api('api/job'); fails = 0; } catch (e) {
      if (++fails < 4) continue;
      polling = false;
      const http = before.find((p) => p.name.startsWith('HTTP'));
      showBanner('与面板失去连接。' + (restartsArgo ? '这个操作重启了 Argo 隧道，如果你是通过 https://xxx.trycloudflare.com 访问的，该地址已失效。' : '') +
        '请稍等片刻后改用 HTTP 入口' + (http ? '：' + http.value : '') + '，或在服务器上执行 vps panel 查看最新地址。');
      return;
    }
    const j = res.data && res.data.job;
    if (!j) continue;
    showJob(j);
    if (j.state !== 'running') break;
  }
  polling = false;
  await afterJob(before);
}

async function afterJob(before) {
  const { ok, data } = await api('api/overview');
  if (!ok) return;
  lastPanel = data.panel || [];
  const changed = JSON.stringify(before.map((p) => p.value)) !== JSON.stringify(lastPanel.map((p) => p.value));
  if (changed) {
    showBanner('面板地址已变化，新的入口：\n' + lastPanel.map((p) => p.name + '：' + p.value).join('\n'));
  }
  if (currentTab === 'overview') loadOverview();
}

function showBanner(text) {
  const b = $('banner');
  b.hidden = false;
  b.textContent = text;
}

// ---------- 规则 ----------
async function loadRules() {
  const { ok, data } = await api('api/rules');
  if (!ok) return;
  $('rules-text').value = data.text;
  $('groups').textContent = '可用策略：DIRECT、REJECT、REJECT-DROP' + (data.groups.length ? '，以及模板里的策略组：' + data.groups.join('、') : '（读取不到模板里的策略组）');
  updateCount();
}
function updateCount() {
  const n = $('rules-text').value.split('\n').filter((l) => l.trim() && !l.trim().startsWith('#')).length;
  $('rules-count').textContent = '当前 ' + n + ' 条规则';
}
$('rules-text').addEventListener('input', updateCount);

$('rules-save').addEventListener('click', async () => {
  const errs = $('rules-errors');
  errs.hidden = true;
  const { ok, status, data } = await post('api/rules', { text: $('rules-text').value });
  if (ok) { watchJob(false); return; }
  if (status === 400 && data && data.errors) {
    errs.replaceChildren(...data.errors.map((e) => h('li', {}, (e.line ? '第 ' + e.line + ' 行：' : '') + e.msg)));
    errs.hidden = false;
  } else {
    alert((data && data.error) || '保存失败（' + status + '）');
  }
});

$('tpl-restore').addEventListener('click', () => {
  if (confirm('从 GitHub 重新下载规则模板，覆盖本机模板。\n自定义规则不受影响。确认？')) runAction('rules', new URLSearchParams());
});

// ---------- 日志 ----------
async function loadLogs() {
  const { ok, data } = await api('api/logs?unit=' + encodeURIComponent($('log-unit').value) + '&n=' + encodeURIComponent($('log-n').value));
  if (!ok) { $('log-out').textContent = (data && data.error) || '读取失败'; return; }
  $('log-out').textContent = data.text;
  $('log-out').scrollTop = $('log-out').scrollHeight;
}
$('log-refresh').addEventListener('click', loadLogs);
$('log-unit').addEventListener('change', loadLogs);
$('log-n').addEventListener('change', loadLogs);

buildOps();
showTab('overview');
