'use strict';

const root = document.getElementById('root');
const modalRoot = document.getElementById('modal-root');
const toastRoot = document.getElementById('toast');
const API_ROOT = new URL('api/', document.baseURI).pathname;

const PAGES = ['overview', 'live', 'rules', 'certs', 'logs', 'settings'];

const S = {
  data: null,
  page: 'overview',
  ruleQuery: '',
  logKind: '',
  logHost: '',
  logDate: '',
  logSource: 'memory',
  logDetailJson: null,
  recentAccess: null,
  liveData: null,
  autoLive: true,
  autoLogs: true,
  testResult: null,
  testHost: '',
  logsData: null,
  confirmCb: null,
  loading: false,
};

/* ---------------- utils ---------------- */
function esc(v) {
  return String(v == null ? '' : v).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function clipText(v, max) {
  const s = String(v == null ? '' : v);
  if (max <= 0 || s.length <= max) return s;
  return s.slice(0, max - 1) + '…';
}

async function copyText(text) {
  const value = String(text == null ? '' : text);
  if (!value) return false;
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(value);
      return true;
    }
  } catch (e) { /* 飞牛 iframe / 权限策略可能拒绝，走 execCommand 兜底 */ }
  try {
    const ta = document.createElement('textarea');
    ta.value = value;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    ta.style.top = '0';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    ta.setSelectionRange(0, value.length);
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return !!ok;
  } catch (e) {
    return false;
  }
}

function customSelect({ name, id = '', value = '', options = [], className = '', placeholder = '请选择' }) {
  const selected = options.find((o) => String(o.value) === String(value)) || options[0] || { value: '', label: placeholder };
  const items = options.map((o) => {
    const active = String(o.value) === String(selected.value);
    return '<button type="button" class="select-option ' + (active ? 'active' : '') + '" data-select-option data-value="' + esc(o.value) + '" data-label="' + esc(o.label) + '" role="option" aria-selected="' + active + '">' +
      '<span>' + esc(o.label) + '</span><span class="select-check">' + (active ? icon('check') : '') + '</span></button>';
  }).join('');
  return '<div class="custom-select ' + className + '" data-custom-select data-name="' + esc(name) + '" data-value="' + esc(selected.value) + '">' +
    '<input type="hidden" name="' + esc(name) + '" ' + (id ? 'id="' + esc(id) + '" ' : '') + 'value="' + esc(selected.value) + '">' +
    '<button type="button" class="select-toggle" data-select-toggle aria-haspopup="listbox" aria-expanded="false">' +
      '<span class="select-label">' + esc(selected.label) + '</span>' +
      '<svg class="select-arrow" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg>' +
    '</button>' +
    '<div class="select-menu" role="listbox">' + items + '</div>' +
  '</div>';
}

function closeSelect(box) {
  if (!box) return;
  box.classList.remove('open');
  const toggle = box.querySelector('[data-select-toggle]');
  if (toggle) toggle.setAttribute('aria-expanded', 'false');
}

function closeAllSelects(except) {
  document.querySelectorAll('[data-custom-select].open').forEach((box) => {
    if (box !== except) closeSelect(box);
  });
}

function setSelectValue(box, value, label) {
  if (!box) return;
  const input = box.querySelector('input[type="hidden"]');
  if (input) input.value = value;
  box.dataset.value = value;
  const labelEl = box.querySelector('.select-label');
  if (labelEl) labelEl.textContent = label || value;
  box.querySelectorAll('[data-select-option]').forEach((opt) => {
    const active = String(opt.dataset.value) === String(value);
    opt.classList.toggle('active', active);
    opt.setAttribute('aria-selected', String(active));
    const check = opt.querySelector('.select-check');
    if (check) check.innerHTML = active ? icon('check') : '';
  });
  closeSelect(box);
  if (box.dataset.name === 'log-date') {
    S.logDate = value;
    S.logsData = null;
    loadLogs();
  }
}

function fmtBuildTime(raw) {
  const d = new Date(raw);
  if (!raw || Number.isNaN(d.getTime())) return raw || '—';
  const p = (n) => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}
const nf = new Intl.NumberFormat('zh-CN');
function fmtNum(n) { return nf.format(Number(n) || 0); }
function fmtBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toFixed(n >= 100 ? 0 : 1) + ' ' + units[i];
}

function fmtSpeed(n) {
  n = Number(n) || 0;
  if (n < 1024) return Math.round(n) + ' B/s';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB/s';
  return (n / 1024 / 1024).toFixed(1) + ' MB/s';
}
function fmtDuration(sec) {
  sec = Number(sec) || 0;
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return d + ' 天 ' + h + ' 时';
  if (h > 0) return h + ' 时 ' + m + ' 分';
  if (m > 0) return m + ' 分';
  return sec + ' 秒';
}
function fmtDate(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d)) return '—';
  return d.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' });
}
function fmtClock(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return '—';
  return d.toLocaleTimeString('zh-CN', { hour12: false });
}
function timeAgo(iso) {
  if (!iso) return '刚刚';
  const t = new Date(iso).getTime();
  if (!t) return '刚刚';
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60) return s + ' 秒前';
  if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
  if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
  return Math.floor(s / 86400) + ' 天前';
}
function codeClass(code) {
  if (code >= 500) return 'code-5xx';
  if (code >= 400) return 'code-4xx';
  if (code >= 300) return 'code-3xx';
  return 'code-2xx';
}
function isWild(d) { return String(d).startsWith('*.'); }
function portText(port) { return Number(port) === 443 ? '' : ':' + port; }

/* ---------------- theme ---------------- */
function savedTheme() {
  try { return localStorage.getItem('fnrelay_theme') || 'auto'; } catch (e) { return 'auto'; }
}
function systemDark() {
  return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
}
function effectiveTheme() {
  const choice = savedTheme();
  if (choice === 'light' || choice === 'dark') return choice;
  return systemDark() ? 'dark' : 'light';
}
function applyTheme(choice, persist) {
  if (persist !== false) {
    try { localStorage.setItem('fnrelay_theme', choice); } catch (e) { /* ignore */ }
  }
  const dark = choice === 'dark' || (choice === 'auto' && systemDark());
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light';
  const meta = document.getElementById('theme-color');
  if (meta) meta.setAttribute('content', dark ? '#05080f' : '#f3f6fb');
  document.querySelectorAll('[data-theme-toggle]').forEach((btn) => {
    btn.innerHTML = icon(dark ? 'sun' : 'moon');
  });
  document.querySelectorAll('[data-theme-choice]').forEach((btn) => {
    btn.classList.toggle('active', btn.dataset.themeChoice === choice);
  });
}

/* ---------------- icons ---------------- */
const PATHS = {
  grid: '<rect x="3" y="3" width="7" height="7" rx="2"/><rect x="14" y="3" width="7" height="7" rx="2"/><rect x="3" y="14" width="7" height="7" rx="2"/><rect x="14" y="14" width="7" height="7" rx="2"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.4 2.6 3.6 5.7 3.6 9S14.4 18.4 12 21c-2.4-2.6-3.6-5.7-3.6-9S9.6 5.6 12 3z"/>',
  shield: '<path d="M12 3l7 3v5.5c0 4.4-2.9 7.7-7 9.5-4.1-1.8-7-5.1-7-9.5V6l7-3z"/><path d="M9.2 12.2l2 2 3.6-3.8"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13"/><circle cx="3.5" cy="6" r="1"/><circle cx="3.5" cy="12" r="1"/><circle cx="3.5" cy="18" r="1"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 00.3 1.9l.1.1a2 2 0 11-2.8 2.8l-.1-.1a1.7 1.7 0 00-1.9-.3 1.7 1.7 0 00-1 1.5V21a2 2 0 11-4 0v-.1a1.7 1.7 0 00-1.1-1.5 1.7 1.7 0 00-1.9.3l-.1.1a2 2 0 11-2.8-2.8l.1-.1a1.7 1.7 0 00.3-1.9 1.7 1.7 0 00-1.5-1H3a2 2 0 110-4h.1a1.7 1.7 0 001.5-1.1 1.7 1.7 0 00-.3-1.9l-.1-.1a2 2 0 112.8-2.8l.1.1a1.7 1.7 0 001.9.3H9a1.7 1.7 0 001-1.5V3a2 2 0 114 0v.1a1.7 1.7 0 001 1.5 1.7 1.7 0 001.9-.3l.1-.1a2 2 0 112.8 2.8l-.1.1a1.7 1.7 0 00-.3 1.9V9a1.7 1.7 0 001.5 1H21a2 2 0 110 4h-.1a1.7 1.7 0 00-1.5 1z"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  refresh: '<path d="M20 11a8 8 0 10-2.3 6.3"/><path d="M20 4v7h-7"/>',
  edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 013 3L7 19l-4 1 1-4z"/>',
  trash: '<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/><path d="M10 11v5M14 11v5"/>',
  x: '<path d="M18 6L6 18M6 6l12 12"/>',
  check: '<path d="M20 6L9 17l-5-5"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/>',
  alert: '<path d="M10.3 3.9L2.6 17a2 2 0 001.7 3h15.4a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z"/><path d="M12 9v4M12 17h.01"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
  arrow: '<path d="M5 12h14M13 6l6 6-6 6"/>',
  activity: '<path d="M3 12h4l2.5-7 5 14 2.5-7h4"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  server: '<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="M11 12l8-8M17 4l3 3M14 7l3 3"/>',
  upload: '<path d="M12 16V4M7 9l5-5 5 5"/><path d="M4 16v3a1 1 0 001 1h14a1 1 0 001-1v-3"/>',
  download: '<path d="M12 4v12M7 11l5 5 5-5"/><path d="M4 20h16"/>',
  swap: '<path d="M7 7h11l-3-3M17 17H6l3 3"/>',
  external: '<path d="M14 4h6v6M20 4l-9 9"/><path d="M20 14v5a1 1 0 01-1 1H5a1 1 0 01-1-1V5a1 1 0 011-1h5"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a1 1 0 011-1h10"/>',
  lock: '<rect x="4" y="10" width="16" height="10" rx="2"/><path d="M8 10V7a4 4 0 118 0v3"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M20.5 14.5A8 8 0 019.5 3.5a8.4 8.4 0 1011 11z"/>',
};
function icon(name, cls = '') {
  return '<svg class="' + cls + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' + (PATHS[name] || '') + '</svg>';
}
function logo(cls = 'brand-mark') {
  return '<svg class="' + cls + '" viewBox="0 0 48 48" fill="none" aria-hidden="true">' +
    '<defs><linearGradient id="fnlg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#38bdf8"/><stop offset=".55" stop-color="#6366f1"/><stop offset="1" stop-color="#a855f7"/></linearGradient></defs>' +
    '<rect x="1" y="1" width="46" height="46" rx="14" fill="url(#fnlg)"/>' +
    '<path d="M14 19h16.5M27 14.5l4.5 4.5-4.5 4.5" stroke="#fff" stroke-width="2.8" stroke-linecap="round" stroke-linejoin="round"/>' +
    '<path d="M34 29H17.5M21 24.5L16.5 29l4.5 4.5" stroke="#fff" stroke-opacity=".82" stroke-width="2.8" stroke-linecap="round" stroke-linejoin="round"/>' +
    '</svg>';
}

/* ---------------- api ---------------- */
async function api(path, opts = {}) {
  const init = {
    method: opts.method || 'GET',
    credentials: 'same-origin',
    headers: { 'X-FnRelay': '1' },
  };
  if (opts.body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body);
  }
  const res = await fetch(API_ROOT + path.replace(/^\//, ''), init);
  let data = {};
  try { data = await res.json(); } catch (e) { data = {}; }
  if (!res.ok) {
    if (res.status === 403 && path !== '/state' && S.data) {
      S.data.authenticated = false;
      renderAuth('仅飞牛管理员可访问，请从飞牛桌面打开。');
    }
    const err = new Error(data.error || ('请求失败（' + res.status + '）'));
    err.status = res.status;
    throw err;
  }
  return data;
}

async function fetchState() {
  const d = await api('/state');
  S.data = d;
  return d;
}

/* ---------------- toast / modal ---------------- */
function toast(msg, type = 'ok') {
  const el = document.createElement('div');
  el.className = 'toast ' + (type === 'err' ? 'err' : 'ok');
  el.innerHTML = icon(type === 'err' ? 'alert' : 'check') + '<span>' + esc(msg) + '</span>';
  toastRoot.appendChild(el);
  setTimeout(() => {
    el.style.transition = 'opacity .3s, transform .3s';
    el.style.opacity = '0';
    el.style.transform = 'translateY(6px)';
    setTimeout(() => el.remove(), 320);
  }, 3400);
}

function openModal(opts) {
  modalRoot.innerHTML =
    '<div class="modal-backdrop" data-modal-bg="1"><div class="modal ' + (opts.wide ? 'wide' : '') + '" role="dialog" aria-modal="true">' +
      '<div class="modal-head"><div><h3 class="modal-title">' + esc(opts.title) + '</h3>' +
        (opts.sub ? '<p class="modal-sub">' + esc(opts.sub) + '</p>' : '') +
      '</div><button class="icon-btn" data-action="modal-close" aria-label="关闭">' + icon('x') + '</button></div>' +
      '<div class="modal-body">' + opts.body + '</div>' +
      (opts.foot ? '<div class="modal-foot">' + opts.foot + '</div>' : '') +
    '</div></div>';
  document.body.style.overflow = 'hidden';
  const first = modalRoot.querySelector('input, textarea, select');
  if (first) setTimeout(() => first.focus(), 60);
}
function closeModal() {
  modalRoot.innerHTML = '';
  document.body.style.overflow = '';
  S.confirmCb = null;
}
function confirmDialog(title, text, cb, okText = '确认删除') {
  S.confirmCb = cb;
  openModal({
    title,
    body: '<p class="muted" style="margin:0;line-height:1.75">' + esc(text) + '</p>',
    foot: '<button class="btn btn-ghost" data-action="modal-close">取消</button>' +
      '<button class="btn btn-danger" data-action="confirm-ok">' + esc(okText) + '</button>',
  });
}

/* ---------------- access ---------------- */
function renderAuth(error) {
  root.innerHTML =
    '<div class="auth"><div class="auth-card">' +
      logo('auth-logo') +
      '<h1 class="auth-title">请从飞牛桌面打开</h1>' +
      '<p class="auth-sub">FnRelay 只通过飞牛桌面内嵌访问，需要飞牛管理员账号，不再单独提供网页端口和密码。</p>' +
      (error ? '<div class="auth-error show">' + esc(error) + '</div>' : '') +
      '<button class="btn btn-primary" data-action="reload">重新加载</button>' +
      '<div class="auth-foot"><span>统一网关 /app/fnrelay</span><span>v' + esc(S.data ? S.data.version : '') + '</span></div>' +
    '</div></div>';
}

/* ---------------- shell ---------------- */
function navItem(key, label, iconName) {
  return '<a class="nav-item ' + (S.page === key ? 'active' : '') + '" href="#/' + key + '">' + icon(iconName, 'nav-icon') + '<span>' + label + '</span></a>';
}

function shell(title, subtitle, content, actions) {
  const st = (S.data && S.data.status) || {};
  const online = !!st.proxy_running;
  return '<div class="app">' +
    '<aside class="sidebar">' +
      '<div class="brand">' + logo() + '<div><div class="brand-name">FnRelay</div><div class="brand-sub">飞牛反向代理</div></div></div>' +
      '<nav class="nav">' +
        '<div class="nav-label">管理</div>' +
        navItem('overview', '概览', 'grid') +
        navItem('live', '实时', 'activity') +
        navItem('rules', '域名规则', 'globe') +
        navItem('certs', '证书', 'shield') +
        navItem('logs', '日志', 'list') +
        navItem('settings', '设置', 'settings') +
      '</nav>' +
      '<div class="sidebar-foot">' +
        '<div class="engine-chip"><span class="status-dot ' + (online ? 'online' : 'offline') + '"></span><div>代理引擎<br><b>' + (online ? '单端口 :' + st.proxy_port : '未运行') + '</b></div></div>' +
        '<div class="sidebar-user"><span>v' + esc(S.data.version) + '</span><span>飞牛桌面</span></div>' +
      '</div>' +
    '</aside>' +
    '<div class="main">' +
      '<header class="topbar">' +
        '<div><h1 class="page-title">' + esc(title) + '</h1><p class="page-sub">' + esc(subtitle) + '</p></div>' +
        '<div class="topbar-actions">' +
          '<button class="icon-btn" data-action="theme-toggle" data-theme-toggle title="切换明暗主题" aria-label="切换明暗主题">' + (effectiveTheme() === 'dark' ? icon('sun') : icon('moon')) + '</button>' +
          '<span class="status-pill ' + (online ? '' : 'err') + '"><span class="status-dot"></span>' + (online ? '运行中' : '未运行') + '</span>' +
          (actions || '') +
        '</div>' +
      '</header>' +
      '<div class="content">' + content + '</div>' +
    '</div>' +
  '</div>';
}

/* ---------------- overview ---------------- */
function viewOverview() {
  const d = S.data;
  const st = d.status;
  const cfg = d.config;
  const certs = d.certs || [];
  const rules = cfg.rules || [];
  const enabledRules = rules.filter((r) => r.enabled);
  const online = st.proxy_running;
  const port = st.proxy_port;
  const banners = [];
  if (d.warning) banners.push(banner('warn', d.warning));
  if (!online && st.proxy_error) banners.push(banner('err', '代理端口没有运行：' + st.proxy_error, true));
  if (st.certs_error) banners.push(banner('warn', '证书加载提示：' + st.certs_error));
  if (!d.detected.fnos_cert_root && !d.detected.fnos_cert_conf) {
    banners.push(banner('info', '没有检测到飞牛证书目录，可以在「证书」页手动导入，或到「设置」里添加证书目录。'));
  }

  const certPreview = certs.filter((c) => !c.builtin).slice().sort((a, b) => new Date(a.not_after) - new Date(b.not_after)).slice(0, 4);
  const recent = S.recentAccess !== null ? S.recentAccess : (d.logs || []).filter((l) => l.kind === 'access').slice(0, 8);
  const recentLogsHTML = '<div id="recent-access">' + recentAccessHTML(recent, !cfg.logs || cfg.logs.access !== false) + '</div>';
  const liveOverview = '<section class="card" style="margin-bottom:16px"><div class="card-head"><div><h3 class="card-title">正在访问</h3><p class="card-sub">实时显示当前经过代理的用户、域名与网速</p></div><a class="card-link" href="#/live">查看实时</a></div>' +
    '<div id="live-preview">' + livePreviewHTML(S.liveData) + '</div></section>';

  const actions = '<button class="btn btn-sm" data-action="cert-reload">' + icon('refresh') + '重载证书</button>' +
    '<button class="btn btn-sm btn-primary" data-action="rule-new">' + icon('plus') + '新建规则</button>';

  return shell('概览', '代理状态、域名规则与证书一目了然', banners.join('') +
    '<section class="card hero">' +
      '<div class="hero-main">' +
        '<div class="hero-kicker">REVERSE PROXY · SINGLE PORT</div>' +
        '<div class="hero-status ' + (online ? '' : 'err') + '" data-live="hero"><span class="status-dot"></span>' + (online ? '运行中' : '需要处理') + '</div>' +
        '<div class="hero-meta">监听 <b>:' + st.proxy_port + '</b><span class="sep">·</span>HTTP 与 HTTPS 自动识别<span class="sep">·</span>飞牛桌面内嵌 /app/fnrelay</div>' +
        '<div class="hero-badges">' +
          '<span class="badge ok"><span class="dot"></span>' + st.rule_count + ' 条规则</span>' +
          '<span class="badge info">' + st.cert_count + ' 张证书</span>' +
          '<span class="badge violet">' + fmtNum(st.tls_requests) + ' HTTPS / ' + fmtNum(st.plain_requests) + ' HTTP</span>' +
          (cfg.proxy.extra_http_port ? '<span class="badge">:' + cfg.proxy.extra_http_port + ' 跳转</span>' : '') +
        '</div>' +
      '</div>' +
      '<div class="hero-side">' +
        '<div class="hero-switch"><div class="hero-switch-label"><b>HTTP → HTTPS</b>' + (cfg.proxy.http_redirect ? '自动跳转已开启' : '当前不跳转') + '</div>' +
          '<label class="switch"><input type="checkbox" data-toggle="http-redirect" ' + (cfg.proxy.http_redirect ? 'checked' : '') + '><span></span></label></div>' +
        '<div class="hero-url">https://<span>你的域名</span>' + portText(port) + (cfg.proxy.redirect_mode === 'custom_port' ? ' <span>→ 跳转 :' + cfg.proxy.redirect_port + '</span>' : '') + '</div>' +
        '<div class="tiny-label">' + (cfg.proxy.redirect_mode === 'custom_port' ? '跳转时改变端口' : '跳转不改变端口') + '</div>' +
      '</div>' +
    '</section>' +
    '<section class="stats">' +
      stat('域名规则', String(st.rule_count), enabledRules.length + ' 条已启用', 'globe', 'rules') +
      stat('证书', String(st.cert_count), '最近加载 ' + timeAgo(st.certs_loaded_at), 'shield', 'certs') +
      stat('累计请求', fmtNum(st.requests), '接收 ' + fmtBytes(st.bytes_in) + ' · 发送 ' + fmtBytes(st.bytes_out), 'activity', 'requests', 'bytes') +
      stat('进程运行', fmtDuration(st.uptime), (st.active_requests || 0) + ' 个活动请求', 'clock', 'uptime', 'active') +
    '</section>' +
    liveOverview +
    '<div class="grid-2" style="margin-bottom:16px">' +
      '<section class="card"><div class="card-head"><div><h3 class="card-title">域名规则</h3><p class="card-sub">按域名或泛域名匹配上游服务</p></div><a class="card-link" href="#/rules">管理全部</a></div>' +
        (enabledRules.length ? '<div class="list">' + enabledRules.slice(0, 5).map((r) =>
          '<div class="list-row"><div style="min-width:0"><div class="row-title">' + esc(r.name) + '</div><div class="row-sub">' + esc(r.domains.slice(0, 2).join(' · ')) + (r.domains.length > 2 ? ' +' + (r.domains.length - 2) : '') + '</div></div>' +
          '<div class="row-side"><span class="badge ok">启用</span></div></div>').join('') + '</div>'
          : emptyBlock('globe', '还没有启用的规则', '添加一条规则，把域名指向 NAS 上的服务')) +
      '</section>' +
      '<section class="card"><div class="card-head"><div><h3 class="card-title">证书状态</h3><p class="card-sub">自动从飞牛证书目录加载并按域名匹配</p></div><a class="card-link" href="#/certs">查看全部</a></div>' +
        (certPreview.length ? '<div class="list">' + certPreview.map((c) => {
          const tone = c.expired || c.days_left < 15 ? 'err' : (c.days_left < 45 ? 'warn' : '');
          const pct = expiryPercent(c);
          return '<div class="list-row"><div style="min-width:0;flex:1"><div class="row-title">' + esc(c.name) + '</div>' +
            '<div class="expiry" style="margin-top:6px"><div class="expiry-bar"><div class="expiry-fill ' + tone + '" style="width:' + pct + '%"></div></div></div></div>' +
            '<div class="row-side"><span class="badge ' + (tone === 'err' ? 'err' : tone === 'warn' ? 'warn' : 'ok') + '">' + (c.expired ? '已过期' : c.days_left + ' 天') + '</span></div></div>';
        }).join('') + '</div>'
          : emptyBlock('shield', '还没有证书', '飞牛「设置 → 安全性 → 证书」上传后将自动出现')) +
      '</section>' +
    '</div>' +
    '<section class="card" style="margin-bottom:16px"><div class="card-head"><div><h3 class="card-title">域名匹配测试</h3><p class="card-sub">输入完整域名，检查会命中哪条规则、使用哪张证书</p></div></div>' +
      '<form class="test-form" data-form="test"><input type="text" name="host" placeholder="app.example.com" value="' + esc(S.testHost) + '" autocomplete="off"><button class="btn btn-primary" type="submit">' + icon('search') + '立即测试</button></form>' +
      (S.testResult ? testResultHTML(S.testResult) : '') +
    '</section>' +
    '<section class="card"><div class="card-head"><div><h3 class="card-title">最近请求</h3><p class="card-sub">最新 8 条访问记录，完整日志在「日志」</p></div><a class="card-link" href="#/logs">全部日志</a></div>' +
      recentLogsHTML +
    '</section>', actions);
}

async function loadRecentAccess() {
  const enabled = !S.data || !S.data.config.logs || S.data.config.logs.access !== false;
  if (!enabled) {
    S.recentAccess = [];
  } else {
    try {
      const q = new URLSearchParams({ kind: 'access', limit: '8', date: serverToday() });
      const r = await api('/logs?' + q.toString());
      S.recentAccess = (r.logs || []).filter((l) => l.kind === 'access');
    } catch (err) {
      S.recentAccess = [];
    }
  }
  const box = document.getElementById('recent-access');
  if (box) box.innerHTML = recentAccessHTML(S.recentAccess, enabled);
}

function recentAccessHTML(recent, enabled = true) {
  if (!recent.length) {
    return enabled
      ? emptyBlock('list', '还没有访问记录', '有请求经过代理后，这里会显示实时日志')
      : emptyBlock('list', '访问记录已关闭', '到「设置 → 日志」打开「记录访问日志」');
  }
  return '<div class="table-wrap"><table><thead><tr><th>时间</th><th>状态</th><th>请求</th><th>规则</th><th>耗时</th></tr></thead><tbody>' +
    recent.map((e) => {
      const req = (e.host || '') + (e.path || '');
      const status = e.status == null ? '—' : e.status;
      return '<tr><td class="num">' + fmtClock(e.time) + '</td><td class="' + codeClass(status) + '">' + status + '</td>' +
        '<td class="mono"><span class="clip recent-request" title="' + esc(req) + '">' + esc(clipText(req, 48)) + '</span></td>' +
        '<td>' + esc(clipText(e.rule || '—', 24)) + '</td><td class="num">' + (e.duration_ms || 0) + ' ms</td></tr>';
    }).join('') +
    '</tbody></table></div>';
}

function stat(label, value, sub, iconName, liveKey, subKey) {
  return '<div class="card stat"><div class="stat-top"><span class="stat-label">' + label + '</span><span class="stat-icon">' + icon(iconName) + '</span></div>' +
    '<div class="stat-value" data-live="' + liveKey + '">' + value + '</div><div class="stat-sub"' + (subKey ? ' data-live-sub="' + subKey + '"' : '') + '>' + sub + '</div></div>';
}
function banner(type, text, settingsLink) {
  return '<div class="banner ' + type + '">' + icon(type === 'err' ? 'alert' : type === 'warn' ? 'alert' : 'info') +
    '<div>' + esc(text) + (settingsLink ? ' <a href="#/settings">去设置</a>' : '') + '</div></div>';
}
function emptyBlock(iconName, title, sub) {
  return '<div class="empty">' + icon(iconName) + '<b>' + esc(title) + '</b><span>' + esc(sub) + '</span></div>';
}
function expiryPercent(c) {
  const start = new Date(c.not_before).getTime();
  const end = new Date(c.not_after).getTime();
  const now = Date.now();
  if (!start || !end || end <= start) return 4;
  const left = Math.max(0, end - now);
  return Math.max(2, Math.min(100, Math.round(left / (end - start) * 100)));
}
function testResultHTML(r) {
  return '<div class="test-result">' +
    '<div class="tr-item"><div class="label">匹配规则</div><div class="value ' + (r.matched ? 'tr-ok' : 'tr-no') + '">' + (r.matched ? esc(r.rule) : '未匹配') + '</div></div>' +
    '<div class="tr-item"><div class="label">上游服务</div><div class="value mono">' + (r.matched ? esc(r.upstream) : '—') + '</div></div>' +
    '<div class="tr-item"><div class="label">使用证书</div><div class="value">' + (r.cert ? esc(r.cert.name) + ' <span class="dim">· ' + r.cert.days_left + ' 天</span>' : '无') + '</div></div>' +
    '<div class="tr-item"><div class="label">HTTPS 地址</div><div class="value mono">' + esc(r.https_url) + '</div></div>' +
  '</div>';
}

/* ---------------- rules ---------------- */
function viewRules() {
  const cfg = S.data.config;
  const rules = cfg.rules || [];
  const selected = rules.filter((r) => {
    if (!S.ruleQuery) return true;
    const q = S.ruleQuery.toLowerCase();
    return (r.name + ' ' + r.upstream + ' ' + r.domains.join(' ')).toLowerCase().includes(q);
  });
  const actions = '<button class="btn btn-sm" data-action="domain-replace">' + icon('swap') + '批量替换域名</button>' +
    '<button class="btn btn-sm btn-primary" data-action="rule-new">' + icon('plus') + '新建规则</button>';
  return shell('域名规则', '精确域名与泛域名都支持，匹配时优先精确域名', 
    '<div class="toolbar"><div class="search">' + icon('search') + '<input id="rule-search" placeholder="搜索域名、名称或上游" value="' + esc(S.ruleQuery) + '"></div>' +
      '<span class="dim" id="rule-count">' + selected.length + ' / ' + rules.length + ' 条</span>' +
      '<div class="spacer"></div><span class="badge info">泛域名：*.example.com</span></div>' +
    (rules.length ? '<div class="rule-grid" id="rule-grid">' + rules.map(ruleCard).join('') + '</div>' : emptyBlock('globe', '还没有规则', '点击右上角「新建规则」开始')),
    actions);
}

function normalizeSuffixInput(raw) {
  let s = String(raw || '').trim().toLowerCase();
  s = s.replace(/^\*\./, '').replace(/^\./, '').replace(/\.$/, '');
  return s;
}

function replaceDomainSuffixJS(domain, oldSuffix, newSuffix) {
  const d = String(domain || '').toLowerCase();
  const wildcard = d.startsWith('*.');
  const base = wildcard ? d.slice(2) : d;
  const prefix = wildcard ? '*.' : '';
  if (base === oldSuffix) return prefix + newSuffix;
  if (base.endsWith('.' + oldSuffix)) return prefix + base.slice(0, -oldSuffix.length) + newSuffix;
  return null;
}

function domainReplaceData() {
  const f = document.querySelector('form[data-form="domain-replace"]');
  if (!f) return { error: '表单不存在' };
  const fd = new FormData(f);
  const oldSuffix = normalizeSuffixInput(fd.get('old_suffix'));
  const newSuffix = normalizeSuffixInput(fd.get('new_suffix'));
  if (!oldSuffix || !newSuffix) return { error: '请输入原后缀和新后缀' };
  if (/[*:/\s]/.test(oldSuffix) || /[*:/\s]/.test(newSuffix)) return { error: '后缀不能包含通配符、端口或路径' };
  if (oldSuffix === newSuffix) return { error: '新旧后缀不能相同' };
  const rules = (S.data.config && S.data.config.rules) || [];
  const changes = [];
  const hitRules = new Set();
  rules.forEach((r) => {
    (r.domains || []).forEach((domain) => {
      const next = replaceDomainSuffixJS(domain, oldSuffix, newSuffix);
      if (next && next !== domain) {
        changes.push({ from: domain, to: next, rule: r.name || r.id });
        hitRules.add(r.id || r.name);
      }
    });
  });
  return { oldSuffix, newSuffix, changes, hitRules: hitRules.size };
}

function updateDomainReplacePreview() {
  const box = document.getElementById('domain-replace-preview');
  if (!box) return;
  const data = domainReplaceData();
  if (data.error) {
    box.innerHTML = '<p class="hint">' + esc(data.error) + '</p>';
    return;
  }
  if (!data.changes.length) {
    box.innerHTML = '<p class="hint">没有匹配 .' + esc(data.oldSuffix) + ' 的域名。</p>';
    return;
  }
  const rows = data.changes.slice(0, 20).map((c) =>
    '<div class="domain-preview-row"><span class="mono">' + esc(c.from) + '</span><span class="domain-arrow">→</span><span class="mono">' + esc(c.to) + '</span><span class="dim">' + esc(c.rule) + '</span></div>'
  ).join('');
  const more = data.changes.length > 20 ? '<div class="hint">还有 ' + (data.changes.length - 20) + ' 个域名未展开</div>' : '';
  box.innerHTML = '<div class="domain-preview-head">影响 <b>' + data.changes.length + '</b> 个域名，涉及 <b>' + data.hitRules + '</b> 条规则</div>' +
    '<div class="domain-preview-list">' + rows + '</div>' + more;
}

function openDomainReplace() {
  openModal({
    title: '批量替换域名',
    sub: '只替换域名后缀，保留前面的子域名；规则名称、上游和证书提示不变',
    wide: true,
    body: '<form data-form="domain-replace">' +
      '<div class="form-grid">' +
        '<div class="field"><label>原域名后缀</label><input type="text" name="old_suffix" placeholder="s.com" autocomplete="off" autofocus></div>' +
        '<div class="field"><label>新域名后缀</label><input type="text" name="new_suffix" placeholder="c.com" autocomplete="off"></div>' +
      '</div>' +
      '<p class="hint" style="margin:0 0 12px">示例：1.s.com → 1.c.com，*.s.com → *.c.com，s.com → c.com；nots.com 不会被误替换。替换后请确认证书覆盖新域名。</p>' +
      '<div id="domain-replace-preview" class="domain-preview"><p class="hint">输入两个后缀后显示预览。</p></div>' +
    '</form>',
    foot: '<button class="btn btn-ghost" data-action="modal-close">取消</button>' +
      '<button class="btn btn-primary" data-action="domain-replace-save">' + icon('swap') + '确认替换</button>',
  });
}

function ruleCard(r) {
  const badges = [];
  if (r.preserve_host) badges.push('<span class="badge info">保留 Host</span>');
  if (r.insecure_skip_verify) badges.push('<span class="badge warn">忽略证书</span>');
  if (r.headers && Object.keys(r.headers).length) badges.push('<span class="badge violet">+ ' + Object.keys(r.headers).length + ' 请求头</span>');
  if (r.cert_hint) badges.push('<span class="badge">证书 ' + esc(r.cert_hint) + '</span>');
  const search = (r.name + ' ' + r.upstream + ' ' + r.domains.join(' ')).toLowerCase();
  const hidden = S.ruleQuery && !search.includes(S.ruleQuery.toLowerCase());
  return '<div class="card rule-card ' + (r.enabled ? '' : 'off') + '" style="' + (hidden ? 'display:none' : '') + '" data-search="' + esc(search) + '">' +
    '<div class="rule-top"><div style="min-width:0"><div class="rule-name">' + esc(r.name) + '</div>' +
      (r.note ? '<div class="rule-note">' + esc(r.note) + '</div>' : '') + '</div>' +
      '<label class="switch" title="启用 / 停用"><input type="checkbox" data-rule-toggle="' + esc(r.id) + '" ' + (r.enabled ? 'checked' : '') + '><span></span></label></div>' +
    '<div class="chips">' + r.domains.map((d) => '<span class="chip ' + (isWild(d) ? 'wild' : '') + '">' + esc(d) + '</span>').join('') + '</div>' +
    '<div class="rule-up"><span>→</span><b>' + esc(r.upstream) + '</b></div>' +
    (badges.length ? '<div class="chips">' + badges.join('') + '</div>' : '') +
    '<div class="rule-foot"><span class="badge ' + (r.enabled ? 'ok' : '') + '">' + (r.enabled ? '已启用' : '已停用') + '</span>' +
      '<div class="rule-actions"><button class="icon-btn" data-action="rule-edit" data-id="' + esc(r.id) + '" title="编辑">' + icon('edit') + '</button>' +
      '<button class="icon-btn" data-action="rule-delete" data-id="' + esc(r.id) + '" title="删除">' + icon('trash') + '</button></div></div>' +
  '</div>';
}

function openRuleModal(rule) {
  const isNew = !rule;
  const cfg = S.data.config;
  const certOptions = [{ value: '', label: '自动匹配（推荐）' }].concat(
    (S.data.certs || []).filter((c) => !c.builtin).map((c) => ({ value: c.name, label: c.name }))
  );
  const body = '<form data-form="rule" data-id="' + esc(isNew ? '' : rule.id) + '">' +
    '<div class="form-grid">' +
      '<div class="field"><label>规则名称</label><input type="text" name="name" value="' + esc(isNew ? '' : rule.name) + '" placeholder="例如：Jellyfin" required></div>' +
      '<div class="field"><label>上游地址</label><input type="text" name="upstream" value="' + esc(isNew ? '' : rule.upstream) + '" placeholder="http://127.0.0.1:8096" required></div>' +
      '<div class="field full"><label>域名（每行一个，支持泛域名）</label><textarea name="domains" placeholder="jellyfin.example.com&#10;*.media.example.com" required>' + esc(isNew ? '' : rule.domains.join('\n')) + '</textarea><div class="hint">精确域名优先于泛域名；泛域名 *.example.com 会匹配任意层级子域名。</div></div>' +
      '<div class="field"><label>指定证书</label>' + customSelect({ name: 'cert_hint', value: isNew ? '' : rule.cert_hint, options: certOptions }) + '<div class="hint">留空时按 SNI 自动选择最合适的证书。</div></div>' +
      '<div class="field"><label>备注</label><input type="text" name="note" value="' + esc(isNew ? '' : rule.note) + '" placeholder="可选"></div>' +
    '</div>' +
    '<div class="divider"></div>' +
    '<div class="toggle-list">' +
      '<label class="toggle-row"><span><span class="t-title">启用规则</span><span class="t-sub">停用后该域名不再转发</span></span><span class="switch"><input type="checkbox" name="enabled" ' + (isNew || rule.enabled ? 'checked' : '') + '><span></span></span></label>' +
      '<label class="toggle-row"><span><span class="t-title">保留 Host</span><span class="t-sub">向上游传递原始域名</span></span><span class="switch"><input type="checkbox" name="preserve_host" ' + (!isNew && rule.preserve_host ? 'checked' : '') + '><span></span></span></label>' +
      '<label class="toggle-row"><span><span class="t-title">忽略上游证书错误</span><span class="t-sub">上游是自签 HTTPS 时使用</span></span><span class="switch"><input type="checkbox" name="insecure_skip_verify" ' + (!isNew && rule.insecure_skip_verify ? 'checked' : '') + '><span></span></span></label>' +
    '</div>' +
  '</form>';
  openModal({
    title: isNew ? '新建规则' : '编辑规则',
    sub: isNew ? '把域名指向 NAS 上的服务，HTTPS 证书自动匹配' : rule.name,
    body,
    foot: '<button class="btn btn-ghost" data-action="modal-close">取消</button>' +
      '<button class="btn btn-primary" data-action="rule-save">' + icon('check') + '保存规则</button>',
  });
}

async function saveRule() {
  const f = document.querySelector('form[data-form="rule"]');
  if (!f) return;
  const fd = new FormData(f);
  const domains = String(fd.get('domains') || '').split(/[\n,，;；]+/).map((s) => s.trim()).filter(Boolean);
  if (!domains.length) return toast('至少填写一个域名', 'err');
  if (!String(fd.get('upstream') || '').trim()) return toast('请填写上游地址', 'err');
  const id = f.dataset.id;
  const payload = {
    name: String(fd.get('name') || '').trim(),
    upstream: String(fd.get('upstream') || '').trim(),
    domains,
    cert_hint: String(fd.get('cert_hint') || ''),
    note: String(fd.get('note') || '').trim(),
    enabled: f.querySelector('[name=enabled]').checked,
    preserve_host: f.querySelector('[name=preserve_host]').checked,
    insecure_skip_verify: f.querySelector('[name=insecure_skip_verify]').checked,
    headers: {},
  };
  try {
    if (id) await api('/rules/' + encodeURIComponent(id), { method: 'PUT', body: payload });
    else await api('/rules', { method: 'POST', body: payload });
    closeModal();
    await refreshAll();
    toast(id ? '规则已更新' : '规则已创建');
  } catch (e) {
    toast(e.message, 'err');
  }
}

/* ---------------- certs ---------------- */
function viewCerts() {
  const certs = S.data.certs || [];
  const d = S.data;
  const real = certs.filter((c) => !c.builtin).length;
  const actions = '<button class="btn btn-sm" data-action="cert-reload">' + icon('refresh') + '重载</button>' +
    '<button class="btn btn-sm btn-primary" data-action="cert-import">' + icon('upload') + '导入证书</button>';
  return shell('证书', '自动加载飞牛系统证书，也可手动导入', 
    '<div class="banner info">' + icon('info') + '<div>自动扫描 <span class="mono">/usr/trim/var/trim_connect/ssls</span>，读取 <span class="mono">network_gateway_cert.conf</span> 中登记的证书；每 30 秒检查一次更新，续签后无需重启。' +
      (d.detected.fnos_cert_root ? '' : ' <b>当前未检测到该目录。</b>') + '</div></div>' +
    '<div class="toolbar"><span class="dim">' + real + ' 张系统 / 手动证书</span><div class="spacer"></div>' +
      '<span class="badge ' + (d.status.certs_error ? 'warn' : 'ok') + '">' + (d.status.certs_error ? '有加载提示' : '加载正常') + '</span></div>' +
    (certs.length ? '<div class="cert-grid">' + certs.map(certCard).join('') + '</div>' : emptyBlock('shield', '还没有可用证书', '到飞牛设置里上传证书，或点击右上角手动导入')),
    actions);
}

function certCard(c) {
  const src = { fnos: '飞牛自动加载', manual: '手动导入', custom: '自定义目录', 'self-signed': '内置自签' }[c.source] || c.source;
  const tone = c.expired || c.days_left < 15 ? 'err' : (c.days_left < 45 ? 'warn' : '');
  const pct = expiryPercent(c);
  return '<div class="card cert-card">' +
    '<div class="cert-top"><div class="cert-icon">' + icon('shield') + '</div><div style="min-width:0"><div class="cert-name">' + esc(c.name) + '</div><div class="cert-source">' + esc(src) + '</div></div>' +
      (c.is_default ? '<span class="badge ok" style="margin-left:auto">默认</span>' : '') + '</div>' +
    '<div class="chips">' + (c.domains || []).slice(0, 6).map((x) => '<span class="chip ' + (isWild(x) ? 'wild' : '') + '">' + esc(x) + '</span>').join('') +
      ((c.domains || []).length > 6 ? '<span class="chip small">+' + (c.domains.length - 6) + '</span>' : '') + '</div>' +
    '<div class="expiry"><div class="expiry-top"><span>' + (c.expired ? '已过期' : '剩余 ' + c.days_left + ' 天') + '</span><span>' + fmtDate(c.not_after) + '</span></div>' +
      '<div class="expiry-bar"><div class="expiry-fill ' + tone + '" style="width:' + pct + '%"></div></div></div>' +
    '<div class="cert-meta"><span title="' + esc(c.issuer || '') + '">' + esc(c.issuer || '—') + '</span>' +
      '<span class="row" style="gap:4px">' +
        (!c.is_default && !c.builtin ? '<button class="btn btn-ghost btn-sm" data-action="cert-default" data-name="' + esc(c.name) + '">设为默认</button>' : '') +
        (c.manual ? '<button class="icon-btn" data-action="cert-delete" data-id="' + esc(c.id) + '" title="删除">' + icon('trash') + '</button>' : '') +
      '</span></div>' +
  '</div>';
}

function openCertImport() {
  const body = '<form data-form="cert-import">' +
    '<div class="field" style="margin-bottom:14px"><label>证书名称（可选）</label><input type="text" name="name" placeholder="例如 example.com"></div>' +
    '<div class="field" style="margin-bottom:14px"><label>证书 PEM</label><textarea name="cert" placeholder="-----BEGIN CERTIFICATE-----" style="min-height:130px" required></textarea></div>' +
    '<div class="field"><label>私钥 PEM</label><textarea name="key" placeholder="-----BEGIN PRIVATE KEY-----" style="min-height:130px" required></textarea><div class="hint">私钥仅保存在 NAS 本地数据目录，不会上传到任何地方。</div></div>' +
  '</form>';
  openModal({
    title: '导入证书',
    sub: '支持完整证书链；PEM 格式',
    body,
    wide: true,
    foot: '<button class="btn btn-ghost" data-action="modal-close">取消</button>' +
      '<button class="btn btn-primary" data-action="cert-import-save">' + icon('check') + '导入</button>',
  });
}

/* ---------------- logs ---------------- */
function todayStr() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate());
}

function serverToday() {
  return (S.data && S.data.log_stats && S.data.log_stats.today) || todayStr();
}

function viewLogs() {
  const entries = S.logsData || (S.data.logs || []);
  S.logsData = entries;
  const today = serverToday();
  const dates = (S.data.log_dates || []).map((x) => x.date);
  const allDates = Array.from(new Set([today].concat(dates))).sort().reverse();
  if (!S.logDate || !allDates.includes(S.logDate)) S.logDate = today;
  const actions = '<button class="btn btn-sm" data-action="log-download">' + icon('download') + '下载</button>' +
    '<button class="btn btn-sm" data-action="log-refresh">' + icon('refresh') + '刷新</button>';
  return shell('日志', '访问、审核与系统事件；本地落盘可选，最长保留 7 天',
    '<div class="toolbar">' +
      '<div class="seg" id="log-kind">' +
        '<button class="' + (S.logKind === '' ? 'active' : '') + '" data-logkind="">全部</button>' +
        '<button class="' + (S.logKind === 'access' ? 'active' : '') + '" data-logkind="access">访问</button>' +
        '<button class="' + (S.logKind === 'audit' ? 'active' : '') + '" data-logkind="audit">审核</button>' +
        '<button class="' + (S.logKind === 'system' ? 'active' : '') + '" data-logkind="system">系统</button>' +
      '</div>' +
      customSelect({
        name: 'log-date',
        id: 'log-date',
        value: S.logDate,
        className: 'log-date',
        options: allDates.map((d) => ({ value: d, label: d === today ? '今天 ' + d : d })),
      }) +
      '<div class="search">' + icon('search') + '<input id="log-host" placeholder="搜索域名/IP/动作" value="' + esc(S.logHost) + '"></div>' +
      '<span class="badge ' + (S.logSource === 'file' ? 'info' : '') + '" id="log-source-badge">' + (S.logSource === 'file' ? '历史文件' : '当前会话') + '</span>' +
      '<label class="row" style="gap:8px;font-size:12px;color:var(--muted);margin-left:auto">自动刷新' +
        '<span class="switch"><input type="checkbox" id="log-auto" ' + (S.autoLogs ? 'checked' : '') + '><span></span></span></label>' +
    '</div>' +
    '<section class="card"><div class="table-wrap"><table><thead><tr><th>时间</th><th>类型</th><th>来源 IP</th><th>状态 / 结果</th><th>耗时</th><th>请求 / 操作</th><th>规则 / 动作</th></tr></thead>' +
      '<tbody id="log-body">' + logRows(entries) + '</tbody></table></div></section>',
    actions);
}

function logTypeBadge(e) {
  if (e.kind === 'audit') return '<span class="badge violet">审核</span>';
  if (e.kind === 'system') return '<span class="badge">系统</span>';
  return '<span class="badge info">访问</span>';
}

function logIP(e) {
  const ip = e.client_ip || e.ip || e.remote_addr || '—';
  let html = '<span class="mono">' + esc(ip) + '</span>';
  if (e.ip_source) html += '<div class="log-sub">' + esc(e.ip_source) + '</div>';
  if (e.x_forwarded_for && e.x_forwarded_for.length) html += '<div class="log-sub" title="' + esc(e.x_forwarded_for.join(', ')) + '">xff: ' + esc(e.x_forwarded_for[0]) + '</div>';
  return html;
}

function logRow(e, i) {
  const attr = ' data-action="log-detail" data-index="' + i + '"';
  if (e.kind === 'system') {
    const badge = e.level === '错误' ? 'err' : e.level === '警告' ? 'warn' : 'info';
    return '<tr class="log-row" ' + attr + '><td class="num">' + fmtClock(e.time) + '</td><td>' + logTypeBadge(e) + '</td><td>—</td>' +
      '<td><span class="badge ' + badge + '">' + esc(e.level) + '</span></td><td class="num">—</td>' +
      '<td colspan="2"><span class="clip log-message" title="' + esc(e.message || '') + '">' + esc(clipText(e.message || '', 60)) + '</span></td></tr>';
  }
  if (e.kind === 'audit') {
    const ok = e.result === 'ok';
    const actor = e.actor ? (e.actor.username || e.actor.uid || '—') : '—';
    const action = e.action || '—';
    const target = e.target || '';
    return '<tr class="log-row" ' + attr + '><td class="num">' + fmtClock(e.time) + '</td><td>' + logTypeBadge(e) + '</td><td>' + logIP(e) + '</td>' +
      '<td><span class="badge ' + (ok ? 'ok' : 'err') + '">' + esc(e.result || '—') + '</span> <span class="dim">' + (e.status || '') + '</span></td>' +
      '<td class="num">—</td>' +
      '<td><span class="mono clip log-operation" title="' + esc(action) + '">' + esc(clipText(action, 22)) + '</span>' +
      '<div class="log-sub clip" title="' + esc(target) + '">' + esc(clipText(target, 32)) + '</div></td>' +
      '<td>' + esc(actor) + '</td></tr>';
  }
  const req = (e.host || '') + (e.path || '');
  const query = e.query || '';
  return '<tr class="log-row" ' + attr + '><td class="num">' + fmtClock(e.time) + '</td><td>' + logTypeBadge(e) + '</td><td>' + logIP(e) + '</td>' +
    '<td class="' + codeClass(e.status) + '">' + (e.status || '—') + '</td>' +
    '<td class="num">' + (e.duration_ms || 0) + ' ms<div class="log-sub">' + fmtBytes(e.bytes) + '</div></td>' +
    '<td class="mono"><span class="clip log-request" title="' + esc(req) + '">' + esc(clipText(req, 36)) + '</span>' +
    (query ? '<div class="log-sub clip log-request-sub" title="?' + esc(query) + '">?' + esc(clipText(query, 40)) + '</div>' : '') + '</td>' +
    '<td><span class="clip" title="' + esc(e.rule || '—') + '">' + esc(clipText(e.rule || '—', 24)) + '</span>' +
    '<div class="log-sub clip" title="' + esc(e.upstream || '') + '">' + esc(clipText(e.upstream || '', 48)) + '</div></td></tr>';
}

function logRows(entries) {
  if (!entries.length) return '<tr><td colspan="7"><div class="empty">' + icon('list') + '<b>暂无日志</b><span>代理请求、管理员操作和系统事件会显示在这里</span></div></td></tr>';
  return entries.map((e, i) => logRow(e, i)).join('');
}

function openLogDetail(index) {
  const e = (S.logsData || [])[index];
  if (!e) return;
  const pretty = JSON.stringify(e, null, 2);
  S.logDetailJson = pretty;
  openModal({
    title: '日志详情',
    sub: (e.kind || '') + (e.request_id ? ' · ' + e.request_id : ''),
    body: '<pre class="log-json">' + esc(pretty) + '</pre>',
    wide: true,
    foot: '<button class="btn btn-ghost" data-action="modal-close">关闭</button>' +
      '<button class="btn" data-action="log-copy" data-index="' + index + '">' + icon('copy') + '复制 JSON</button>',
  });
}

async function loadLogs(silent) {
  const q = new URLSearchParams({ limit: '500' });
  if (S.logKind) q.set('kind', S.logKind);
  if (S.logHost) q.set('q', S.logHost);
  if (S.logDate) q.set('date', S.logDate);
  try {
    const r = await api('/logs?' + q.toString());
    S.logsData = r.logs || [];
    S.logSource = r.source || 'memory';
    const body = document.getElementById('log-body');
    if (body) body.innerHTML = logRows(S.logsData);
    const badge = document.querySelector('#log-source-badge');
    if (badge) {
      badge.textContent = S.logSource === 'file' ? '历史文件' : '当前会话';
      badge.className = 'badge ' + (S.logSource === 'file' ? 'info' : '');
    }
  } catch (e) {
    S.logsData = [];
    const body = document.getElementById('log-body');
    if (body) body.innerHTML = logRows([]);
    if (!silent) toast(e.message, 'err');
  }
}

/* ---------------- live ---------------- */
function emptyLive() {
  return { active: 0, down_bps: 0, up_bps: 0, requests: [] };
}

function liveSummaryHTML(d) {
  d = d || emptyLive();
  return '<span class="badge ' + (d.active ? 'ok' : '') + '">活动 ' + (d.active || 0) + '</span>' +
    '<span class="badge info">↓ ' + fmtSpeed(d.down_bps) + '</span>' +
    '<span class="badge violet">↑ ' + fmtSpeed(d.up_bps) + '</span>';
}

function liveStat(label, value, sub, iconName, key) {
  return '<div class="card stat"><div class="stat-top"><span class="stat-label">' + label + '</span><span class="stat-icon">' + icon(iconName) + '</span></div>' +
    '<div class="stat-value" data-live-stat="' + key + '">' + value + '</div><div class="stat-sub">' + sub + '</div></div>';
}

function liveRows(entries) {
  if (!entries || !entries.length) {
    return '<tr><td colspan="8"><div class="empty">' + icon('activity') + '<b>当前没有正在访问的请求</b><span>有请求经过代理时会实时显示在这里</span></div></td></tr>';
  }
  return entries.map((e) => {
    const host = e.host || '—';
    const upstream = e.upstream_host || e.upstream || '—';
    const path = e.path || '/';
    const status = e.status ? e.status : '—';
    const q = e.query || '';
    return '<tr>' +
      '<td>' + logIP(e) + '</td>' +
      '<td><span class="mono clip live-host" title="' + esc(host) + '">' + esc(clipText(host, 32)) + '</span>' +
        (e.tls ? '<div class="log-sub">HTTPS</div>' : '') + '</td>' +
      '<td class="mono"><span class="clip live-path" title="' + esc(path) + '">' + esc(clipText(path, 40)) + '</span>' +
        (q ? '<div class="log-sub clip live-path" title="?' + esc(q) + '">?' + esc(clipText(q, 36)) + '</div>' : '') + '</td>' +
      '<td class="' + codeClass(e.status) + '">' + status + '</td>' +
      '<td class="num"><span class="live-speed down">↓ ' + fmtSpeed(e.down_bps) + '</span><div class="log-sub">' + fmtBytes(e.down_bytes) + '</div></td>' +
      '<td class="num"><span class="live-speed up">↑ ' + fmtSpeed(e.up_bps) + '</span><div class="log-sub">' + fmtBytes(e.up_bytes) + '</div></td>' +
      '<td class="num">' + fmtDuration(Math.floor((e.duration_ms || 0) / 1000)) + '</td>' +
      '<td><span class="mono clip live-upstream" title="' + esc(upstream) + '">' + esc(clipText(upstream, 32)) + '</span></td>' +
    '</tr>';
  }).join('');
}

function livePreviewHTML(d) {
  d = d || emptyLive();
  if (!d.active) return '<div class="live-empty">当前没有正在访问的请求</div>';
  const rows = d.requests.slice(0, 4).map((e) => {
    return '<div class="live-preview-row">' +
      '<span class="live-preview-ip mono">' + esc(e.client_ip || '—') + '</span>' +
      '<span class="live-preview-host"><b>' + esc(clipText(e.host || '—', 28)) + '</b><span>' + esc(clipText(e.path || '/', 34)) + '</span></span>' +
      '<span class="live-preview-speed down">↓ ' + fmtSpeed(e.down_bps) + '</span>' +
      '<span class="live-preview-speed up">↑ ' + fmtSpeed(e.up_bps) + '</span>' +
    '</div>';
  }).join('');
  const more = d.requests.length > 4 ? '<div class="live-more">还有 ' + (d.requests.length - 4) + ' 条，去「实时」查看全部</div>' : '';
  return '<div class="live-summary">' + liveSummaryHTML(d) + '</div><div class="live-preview-list">' + rows + more + '</div>';
}

function viewLive() {
  const d = S.liveData || emptyLive();
  const actions = '<button class="btn btn-sm" data-action="live-refresh">' + icon('refresh') + '刷新</button>';
  return shell('实时访问', '正在经过代理的请求与实时网速，只显示进行中的请求',
    '<section class="stats live-stats">' +
      liveStat('活动请求', d.active || 0, '当前正在转发', 'activity', 'active') +
      liveStat('总下行', fmtSpeed(d.down_bps), '所有活动请求', 'download', 'down') +
      liveStat('总上行', fmtSpeed(d.up_bps), '所有活动请求', 'upload', 'up') +
    '</section>' +
    '<div class="toolbar">' +
      '<span class="live-summary" id="live-summary">' + liveSummaryHTML(d) + '</span>' +
      '<label class="row" style="gap:8px;font-size:12px;color:var(--muted);margin-left:auto">自动刷新 1 秒' +
        '<span class="switch"><input type="checkbox" id="live-auto" ' + (S.autoLive ? 'checked' : '') + '><span></span></span></label>' +
      '<span class="dim">只显示进行中的请求，完成后去「日志」查看</span>' +
    '</div>' +
    '<section class="card"><div class="table-wrap"><table><thead><tr>' +
      '<th>用户 IP</th><th>服务域名</th><th>路径</th><th>状态</th><th>下行</th><th>上行</th><th>持续</th><th>上游</th>' +
    '</tr></thead><tbody id="live-body">' + liveRows(d.requests) + '</tbody></table></div></section>',
    actions);
}

async function loadLive(silent) {
  if (!S.data || !S.data.authenticated) return;
  try {
    S.liveData = await api('/live');
    updateLiveDOM(S.liveData);
  } catch (err) {
    if (!silent) toast(err.message, 'err');
  }
}

async function loadStats(silent) {
  if (!S.data || !S.data.authenticated) return;
  try {
    const st = await api('/stats');
    const values = {
      requests: fmtNum(st.requests),
      uptime: fmtDuration(st.uptime),
      rules: String(st.rule_count),
      certs: String(st.cert_count),
    };
    document.querySelectorAll('[data-live]').forEach((el) => {
      const v = values[el.dataset.live];
      if (v != null) el.textContent = v;
    });
    const subs = {
      bytes: '接收 ' + fmtBytes(st.bytes_in) + ' · 发送 ' + fmtBytes(st.bytes_out),
      active: (st.active_requests != null ? st.active_requests : st.active_conns) + ' 个活动请求',
    };
    document.querySelectorAll('[data-live-sub]').forEach((el) => {
      const v = subs[el.dataset.liveSub];
      if (v != null) el.textContent = v;
    });
  } catch (err) {
    if (!silent) toast(err.message, 'err');
  }
}

function updateLiveDOM(d) {
  d = d || emptyLive();
  const values = {
    active: String(d.active || 0),
    down: fmtSpeed(d.down_bps),
    up: fmtSpeed(d.up_bps),
  };
  document.querySelectorAll('[data-live-stat]').forEach((el) => {
    const v = values[el.dataset.liveStat];
    if (v != null) el.textContent = v;
  });
  const sum = document.getElementById('live-summary');
  if (sum) sum.innerHTML = liveSummaryHTML(d);
  const body = document.getElementById('live-body');
  if (body) body.innerHTML = liveRows(d.requests);
  const preview = document.getElementById('live-preview');
  if (preview) preview.innerHTML = livePreviewHTML(d);
}

/* ---------------- settings ---------------- */
function viewSettings() {
  const d = S.data;
  const cfg = d.config;
  const st = d.status;
  const certs = (d.certs || []).filter((c) => !c.builtin);
  const certOptions = [{ value: '', label: '自动（按域名匹配）' }].concat(
    certs.map((c) => ({ value: c.name, label: c.name }))
  );
  const samePort = cfg.proxy.redirect_mode !== 'custom_port';
  const logsCfg = cfg.logs || { file_enabled: false, access: true, audit: true, retention_days: 7, max_file_mb: 100 };
  const logStats = d.log_stats || {};
  const backupCard = '<section class="card"><div class="card-head"><div><h3 class="card-title">备份与恢复</h3><p class="card-sub">保存设置、端口、证书配置和域名规则</p></div><span class="badge">v' + esc(d.version) + '</span></div>' +
    '<p class="hint" style="margin:0 0 16px">备份文件包含代理/证书/日志设置和全部域名规则，不包含证书私钥。恢复时必须版本完全一致，且会覆盖当前设置和规则。</p>' +
    '<div class="row"><button class="btn" data-action="backup-download">' + icon('download') + '下载备份</button>' +
      '<button class="btn btn-primary" data-action="backup-pick">' + icon('upload') + '选择备份文件恢复</button>' +
      '<input type="file" id="backup-file" accept="application/json,.json" style="display:none">' +
      '<span class="dim" style="margin-left:auto">当前版本 v' + esc(d.version) + '</span></div>' +
  '</section>';

  const logsCard = '<section class="card"><div class="card-head"><div><h3 class="card-title">日志</h3><p class="card-sub">访问、审核、系统事件；本地可选落盘</p></div><span class="badge ' + (logsCfg.file_enabled ? 'ok' : '') + '">' + (logsCfg.file_enabled ? '本地保存开启' : '仅内存') + '</span></div>' +
    '<div class="toggle-list">' +
      '<label class="toggle-row"><span><span class="t-title">保存日志到本地</span><span class="t-sub">按天写入 JSONL，最长保留 7 天；默认关闭</span></span><span class="switch"><input type="checkbox" name="log_file_enabled" ' + (logsCfg.file_enabled ? 'checked' : '') + '><span></span></span></label>' +
      '<label class="toggle-row"><span><span class="t-title">记录访问日志</span><span class="t-sub">包含访问 IP、来源 IP、状态、上游、耗时、UA</span></span><span class="switch"><input type="checkbox" name="log_access" ' + (logsCfg.access ? 'checked' : '') + '><span></span></span></label>' +
      '<label class="toggle-row"><span><span class="t-title">记录审核日志</span><span class="t-sub">规则、设置、证书操作，含操作者与 IP</span></span><span class="switch"><input type="checkbox" name="log_audit" ' + (logsCfg.audit ? 'checked' : '') + '><span></span></span></label>' +
    '</div>' +
    '<div class="form-grid" style="margin-top:16px">' +
      '<div class="field"><label>保留天数（1-7）</label><input type="number" name="log_retention" min="1" max="7" value="' + (logsCfg.retention_days || 7) + '"><div class="hint">跨天自动切文件，启动时清理过期文件。</div></div>' +
      '<div class="field"><label>单日单文件上限（MB）</label><input type="number" name="log_max_mb" min="1" max="1024" value="' + (logsCfg.max_file_mb || 100) + '"><div class="hint">超过后当天停止写入并计入丢弃数，避免写满磁盘。</div></div>' +
    '</div>' +
    '<div class="hint" style="margin-top:12px">目录：<span class="mono">' + esc(logStats.dir || '未初始化') + '</span> · 今日访问 ' + fmtBytes(logStats.today_access_bytes || 0) + ' / 审核 ' + fmtBytes(logStats.today_audit_bytes || 0) + ' / 系统 ' + fmtBytes(logStats.today_system_bytes || 0) +
      (logStats.dropped ? ' · <b style="color:var(--amber)">已丢弃 ' + logStats.dropped + ' 条</b>' : '') +
      (logStats.last_error ? ' · <b style="color:var(--red)">' + esc(logStats.last_error) + '</b>' : '') + '</div>' +
  '</section>';

  const settingsForm = '<form data-form="settings"><div class="stack">' +
    '<section class="card"><div class="card-head"><div><h3 class="card-title">代理与跳转</h3><p class="card-sub">先选监听端口，再决定 HTTP 怎么跳到 HTTPS</p></div><span class="badge info">HTTP/HTTPS 共用</span></div>' +
      '<div class="card-sub" style="margin:-4px 0 12px;font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim)">1 · 入口</div>' +
      '<div class="form-grid">' +
        '<div class="field"><label>代理端口</label><input type="number" name="port" min="1" max="65535" value="' + cfg.proxy.port + '"><div class="hint">FnRelay 在本机监听哪个端口，HTTP 和 HTTPS 共用。标准 HTTPS 用 443；若被飞牛占用，先到「系统设置 → 安全性 → 端口设置 → 高级设置」取消勾选「重定向 80 和 443 端口」。</div></div>' +
        '<div class="field"><label>额外 HTTP 跳转端口（可选）</label><input type="number" name="extra_http_port" min="0" max="65535" value="' + cfg.proxy.extra_http_port + '"><div class="hint">再开一个只负责跳转的 HTTP 入口，不转发网页内容。例如 80：访问 http://域名 会跳 HTTPS。该入口命中规则时始终 308，不受下方开关控制；填 0 关闭。</div></div>' +
        '<div class="field full"><label>默认上游（可选）</label><input type="text" name="default_upstream" value="' + esc(cfg.proxy.default_upstream) + '" placeholder="http://127.0.0.1:8080"><div class="hint">没有命中任何域名规则时，请求转发到这里；留空返回 404。</div></div>' +
      '</div>' +
      '<div class="divider"></div>' +
      '<div class="card-sub" style="margin:-4px 0 12px;font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim)">2 · HTTP → HTTPS</div>' +
      '<div class="row" style="justify-content:space-between;margin-bottom:14px"><div><b style="font-size:13px">HTTP 自动跳转 HTTPS</b><div class="hint">主代理端口收到明文 HTTP 时返回 308；额外 HTTP 端口始终跳转。</div></div>' +
        '<label class="switch"><input type="checkbox" name="http_redirect" ' + (cfg.proxy.http_redirect ? 'checked' : '') + '><span></span></label></div>' +
      '<div class="radio-cards">' +
        '<label class="radio-card"><input type="radio" name="redirect_mode" value="same_port" ' + (samePort ? 'checked' : '') + '><div class="rc-body"><div class="rc-title">保持用户访问的端口（推荐）</div><div class="rc-sub">用户地址带端口就保持该端口，例如 http://域名:7777 → https://域名:7777；地址不带端口时跳到 443。</div></div></label>' +
        '<label class="radio-card"><input type="radio" name="redirect_mode" value="custom_port" ' + (!samePort ? 'checked' : '') + '><div class="rc-body"><div class="rc-title">固定跳到指定端口</div><div class="rc-sub">不管用户从哪个端口访问，都跳到下面填的 HTTPS 端口。适合 HTTP 和 HTTPS 对外端口不同的情况。</div></div></label>' +
      '</div>' +
      '<div class="field" id="redirect-port-field" style="margin-top:14px;max-width:240px;' + (samePort ? 'display:none' : '') + '"><label>固定跳转的 HTTPS 端口</label><input type="number" name="redirect_port" min="1" max="65535" value="' + cfg.proxy.redirect_port + '"></div>' +
      '<div class="divider"></div>' +
      '<div class="card-sub" style="margin:-4px 0 12px;font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--dim)">3 · 安全</div>' +
      '<div class="toggle-list">' +
        '<label class="toggle-row"><span><span class="t-title">HSTS（强制 HTTPS）</span><span class="t-sub">让浏览器以后只走 HTTPS；端口没最终确定前不要开启，开启后很难回退。</span></span><span class="switch"><input type="checkbox" name="hsts" ' + (cfg.proxy.hsts ? 'checked' : '') + '><span></span></span></label>' +
      '</div>' +
    '</section>' +
    '<section class="card"><div class="card-head"><div><h3 class="card-title">证书来源</h3><p class="card-sub">飞牛证书自动加载 + 自定义目录</p></div><span class="badge ' + (cfg.certs.auto_fnos ? 'ok' : '') + '">' + (cfg.certs.auto_fnos ? '自动加载开启' : '自动加载关闭') + '</span></div>' +
      '<div class="toggle-list" style="margin-bottom:16px">' +
        '<label class="toggle-row"><span><span class="t-title">自动加载飞牛证书</span><span class="t-sub">扫描 /usr/trim/var/trim_connect/ssls 和证书配置</span></span><span class="switch"><input type="checkbox" name="auto_fnos" ' + (cfg.certs.auto_fnos ? 'checked' : '') + '><span></span></span></label>' +
      '</div>' +
      '<div class="form-grid">' +
        '<div class="field full"><label>附加证书目录（每行一个，可选）</label><textarea name="extra_dirs" placeholder="/vol1/@appdata/myapp/certs">' + esc((cfg.certs.extra_dirs || []).join('\n')) + '</textarea><div class="hint">目录内 .crt / .pem 与同名 .key 会被自动配对。</div></div>' +
        '<div class="field"><label>默认证书</label>' + customSelect({ name: 'default_cert', value: cfg.certs.default_cert, options: certOptions }) + '<div class="hint">SNI 没匹配到证书时使用。</div></div>' +
      '</div>' +
    '</section>' +
    logsCard +
    '<div class="row"><button class="btn btn-primary" type="submit">' + icon('check') + '保存设置</button><span class="dim">保存后代理会按需重启，当前页面不会断开</span></div>' +
  '</div></form>';

  const themeChoice = savedTheme();
  const appearance = '<section class="card"><div class="card-head"><div><h3 class="card-title">外观</h3><p class="card-sub">跟随系统，或固定亮色 / 暗色</p></div><span class="badge info">当前 ' + (effectiveTheme() === 'dark' ? '暗色' : '亮色') + '</span></div>' +
    '<div class="seg theme-seg">' +
      [['auto', '跟随系统'], ['light', '亮色'], ['dark', '暗色']].map(([value, label]) =>
        '<button data-theme-choice="' + value + '" class="' + (themeChoice === value ? 'active' : '') + '">' + label + '</button>').join('') +
    '</div>' +
    '<p class="hint" style="margin:12px 0 0">主题只保存在当前浏览器，不影响其他用户。</p>' +
  '</section>';

  const about = '<section class="card"><div class="card-head"><div><h3 class="card-title">关于</h3><p class="card-sub">FnRelay · 飞牛原生反向代理</p></div><span class="badge violet">v' + esc(d.version) + '</span></div>' +
    '<div class="list">' +
      '<div class="list-row"><span class="muted">代理端口</span><span class="spacer"></span><span class="mono">:' + st.proxy_port + ' · HTTP/HTTPS 共用</span></div>' +
      '<div class="list-row"><span class="muted">桌面入口</span><span class="spacer"></span><span class="mono">飞牛桌面 → FnRelay（/app/fnrelay）</span></div>' +
      '<div class="list-row"><span class="muted">访问权限</span><span class="spacer"></span><span class="badge ok">飞牛管理员 · 跟随登录态</span></div>' +
      '<div class="list-row"><span class="muted">HTTP / HTTPS</span><span class="spacer"></span><span class="badge ok">TLS / 明文自动分流</span></div>' +
      '<div class="list-row"><span class="muted">证书来源</span><span class="spacer"></span><span>' + (d.detected.fnos_cert_root ? '飞牛证书目录 · 自动扫描' : '未发现飞牛证书目录') + '</span></div>' +
      '<div class="list-row"><span class="muted">主机名</span><span class="spacer"></span><span class="mono">' + esc(d.hostname || '—') + '</span></div>' +
      '<div class="list-row"><span class="muted">构建时间</span><span class="spacer"></span><span class="mono">' + esc(fmtBuildTime(d.build_time)) + '</span></div>' +
    '</div>' +
    '<p class="hint" style="margin:14px 0 0">管理界面仅通过飞牛统一网关访问，不开放独立端口，也不使用 FnRelay 密码；权限跟随飞牛登录态，仅管理员可用。</p>' +
  '</section>';

  return shell('设置', '端口、跳转方式、证书来源与安全选项', 
    '<div class="banner ' + (st.proxy_running ? 'info' : 'err') + '">' + icon(st.proxy_running ? 'info' : 'alert') +
      '<div>' + (st.proxy_running ? '当前代理监听 <b>:' + st.proxy_port + '</b>，' + (cfg.proxy.http_redirect ? 'HTTP 会自动跳转 HTTPS。' : 'HTTP 不会自动跳转。') : '代理未运行：' + esc(st.proxy_error || '未知原因')) + '</div></div>' +
    settingsForm + '<div style="height:16px"></div>' + backupCard + '<div style="height:16px"></div>' + appearance + '<div style="height:16px"></div>' + about,
    '');
}

/* ---------------- render ---------------- */
function render(resetScroll) {
  if (!S.data) return;
  if (!S.data.authenticated) {
    renderAuth();
    return;
  }
  const y = window.scrollY;
  let html = '';
  if (S.page === 'overview') html = viewOverview();
  else if (S.page === 'live') html = viewLive();
  else if (S.page === 'rules') html = viewRules();
  else if (S.page === 'certs') html = viewCerts();
  else if (S.page === 'logs') html = viewLogs();
  else html = viewSettings();
  root.innerHTML = html;
  if (resetScroll) window.scrollTo(0, 0);
  else window.scrollTo(0, y);
}

function currentPage() {
  const h = location.hash.replace(/^#\/?/, '');
  return PAGES.includes(h) ? h : 'overview';
}

async function refreshAll() {
  await fetchState();
  render();
  if (S.page === 'logs') await loadLogs(true);
  if (S.page === 'overview') await loadRecentAccess();
  if (S.page === 'overview' || S.page === 'live') await loadLive(true);
}

/* ---------------- boot ---------------- */
async function boot() {
  try {
    const d = await fetchState();
    if (!d.authenticated) {
      renderAuth();
      return;
    }
    S.page = currentPage();
    if (!location.hash) history.replaceState(null, '', '#/overview');
    render();
    if (S.page === 'overview') await loadRecentAccess();
    if (S.page === 'overview' || S.page === 'live') await loadLive(true);
    if (S.page === 'logs') await loadLogs(true);
  } catch (e) {
    if (e.status === 403) {
      renderAuth(e.message);
      return;
    }
    root.innerHTML = '<div class="auth"><div class="auth-card"><h1 class="auth-title">无法连接</h1><p class="auth-sub">' + esc(e.message) + '</p><button class="btn btn-primary" data-action="reload">重新加载</button></div></div>';
  }
}

/* ---------------- events ---------------- */
document.addEventListener('click', async (e) => {
  const selectToggle = e.target.closest('[data-select-toggle]');
  if (selectToggle) {
    const box = selectToggle.closest('[data-custom-select]');
    const wasOpen = box.classList.contains('open');
    closeAllSelects();
    if (!wasOpen) {
      box.classList.add('open');
      selectToggle.setAttribute('aria-expanded', 'true');
    }
    return;
  }
  const selectOption = e.target.closest('[data-select-option]');
  if (selectOption) {
    setSelectValue(selectOption.closest('[data-custom-select]'), selectOption.dataset.value, selectOption.dataset.label);
    return;
  }
  if (!e.target.closest('[data-custom-select]')) closeAllSelects();

  const bg = e.target.closest('[data-modal-bg]');
  if (bg && e.target === bg) return closeModal();

  const el = e.target.closest('[data-action]');
  if (!el) return;
  const action = el.dataset.action;

  if (action === 'reload') { location.reload(); return; }
  if (action === 'theme-toggle') {
    applyTheme(effectiveTheme() === 'dark' ? 'light' : 'dark');
    return;
  }
  if (action === 'logout') {
    e.preventDefault();
    try { await api('/logout', { method: 'POST' }); } catch (err) { /* ignore */ }
    S.data = null;
    renderAuth();
    return;
  }
  if (action === 'modal-close') return closeModal();
  if (action === 'confirm-ok') {
    const cb = S.confirmCb;
    closeModal();
    if (cb) await cb();
    return;
  }
  if (action === 'domain-replace') return openDomainReplace();
  if (action === 'domain-replace-save') {
    const data = domainReplaceData();
    if (data.error) { toast(data.error, 'err'); return; }
    if (!data.changes.length) { toast('没有匹配的域名', 'err'); return; }
    el.disabled = true;
    try {
      const r = await api('/domains/replace', { method: 'POST', body: { old_suffix: data.oldSuffix, new_suffix: data.newSuffix } });
      closeModal();
      await refreshAll();
      toast('已替换 ' + r.changed_domains + ' 个域名 / ' + r.changed_rules + ' 条规则');
    } catch (err) {
      toast(err.message, 'err');
    }
    el.disabled = false;
    return;
  }
  if (action === 'rule-new') {
    if (S.page !== 'rules') { S.page = 'rules'; location.hash = '#/rules'; render(true); }
    openRuleModal(null);
    return;
  }
  if (action === 'rule-edit') {
    const rule = (S.data.config.rules || []).find((r) => r.id === el.dataset.id);
    if (rule) openRuleModal(rule);
    return;
  }
  if (action === 'rule-delete') {
    const rule = (S.data.config.rules || []).find((r) => r.id === el.dataset.id);
    if (!rule) return;
    confirmDialog('删除规则', '确定删除「' + rule.name + '」吗？该域名的请求会立即停止转发。', async () => {
      try {
        await api('/rules/' + encodeURIComponent(rule.id), { method: 'DELETE' });
        await refreshAll();
        toast('规则已删除');
      } catch (err) { toast(err.message, 'err'); }
    });
    return;
  }
  if (action === 'rule-save') return saveRule();
  if (action === 'cert-reload') {
    el.disabled = true;
    try {
      const r = await api('/certs/reload', { method: 'POST' });
      await refreshAll();
      toast('证书已重新加载' + (r.warning ? '（部分文件有问题）' : ''));
    } catch (err) { toast(err.message, 'err'); }
    el.disabled = false;
    return;
  }
  if (action === 'cert-import') return openCertImport();
  if (action === 'cert-import-save') {
    const f = document.querySelector('form[data-form="cert-import"]');
    if (!f) return;
    const fd = new FormData(f);
    try {
      await api('/certs/import', { method: 'POST', body: {
        name: String(fd.get('name') || '').trim(),
        cert: String(fd.get('cert') || ''),
        key: String(fd.get('key') || ''),
      } });
      closeModal();
      await refreshAll();
      toast('证书已导入');
    } catch (err) { toast(err.message, 'err'); }
    return;
  }
  if (action === 'cert-delete') {
    const cert = (S.data.certs || []).find((c) => c.id === el.dataset.id);
    if (!cert) return;
    confirmDialog('删除证书', '确定删除手动导入的「' + cert.name + '」吗？', async () => {
      try {
        await api('/certs/' + encodeURIComponent(cert.id), { method: 'DELETE' });
        await refreshAll();
        toast('证书已删除');
      } catch (err) { toast(err.message, 'err'); }
    });
    return;
  }
  if (action === 'cert-default') {
    const name = el.dataset.name;
    try {
      await api('/settings', { method: 'POST', body: { proxy: S.data.config.proxy, certs: Object.assign({}, S.data.config.certs, { default_cert: name }) } });
      await refreshAll();
      toast('默认证书已切换');
    } catch (err) { toast(err.message, 'err'); }
    return;
  }
  if (action === 'log-refresh') { await loadLogs(); return; }
  if (action === 'live-refresh') { await loadLive(); return; }
  if (action === 'backup-download') { window.location.href = API_ROOT + 'backup'; return; }
  if (action === 'backup-pick') {
    const input = document.getElementById('backup-file');
    if (input) input.click();
    return;
  }
  if (action === 'log-detail') { openLogDetail(Number(el.dataset.index)); return; }
  if (action === 'log-copy') {
    const e = (S.logsData || [])[Number(el.dataset.index)];
    const text = S.logDetailJson || (e ? JSON.stringify(e, null, 2) : '');
    if (!text) return;
    const ok = await copyText(text);
    toast(ok ? '已复制日志 JSON' : '复制失败，请手动选择 JSON 文本', ok ? 'ok' : 'err');
    return;
  }
  if (action === 'log-download') {
    const kind = S.logKind || 'access';
    const date = S.logDate || serverToday();
    window.open(API_ROOT + 'logs/download?date=' + encodeURIComponent(date) + '&kind=' + encodeURIComponent(kind), '_blank');
    return;
  }
  if (action === 'log-kind') { /* handled below via data-logkind */ }
  if (action === 'copy') {
    const text = el.dataset.copy || '';
    const ok = await copyText(text);
    toast(ok ? '已复制' : '复制失败，请手动选择文本', ok ? 'ok' : 'err');
    return;
  }
});

document.addEventListener('change', async (e) => {
  const t = e.target;
  if (t.matches('[data-toggle="http-redirect"]')) {
    const cfg = S.data.config;
    try {
      await api('/settings', { method: 'POST', body: { proxy: Object.assign({}, cfg.proxy, { http_redirect: t.checked }), certs: cfg.certs } });
      await refreshAll();
      toast(t.checked ? '已开启 HTTP 跳转' : '已关闭 HTTP 跳转');
    } catch (err) { toast(err.message, 'err'); t.checked = !t.checked; }
    return;
  }
  if (t.matches('[data-rule-toggle]')) {
    try {
      await api('/rules/' + encodeURIComponent(t.dataset.ruleToggle) + '/toggle', { method: 'POST', body: { enabled: t.checked } });
      await refreshAll();
      toast(t.checked ? '规则已启用' : '规则已停用');
    } catch (err) { toast(err.message, 'err'); t.checked = !t.checked; }
    return;
  }
  if (t.id === 'log-auto') { S.autoLogs = t.checked; return; }
  if (t.id === 'live-auto') { S.autoLive = t.checked; if (S.autoLive) loadLive(); return; }
  if (t.id === 'backup-file') {
    const file = t.files && t.files[0];
    t.value = '';
    if (!file) return;
    try {
      const data = JSON.parse(await file.text());
      if (data.format !== 'fnrelay-backup' || data.schema !== 1) {
        toast('不是有效的 FnRelay 备份文件', 'err');
        return;
      }
      if (String(data.version) !== String(S.data.version)) {
        toast('备份版本 ' + data.version + '，当前 ' + S.data.version + '，版本必须一致才能恢复。', 'err');
        return;
      }
      if (!data.config || !Array.isArray(data.config.rules)) {
        toast('备份文件缺少配置或域名规则', 'err');
        return;
      }
      const rules = data.config.rules.length;
      confirmDialog('恢复备份', '将用备份 v' + data.version + ' 覆盖当前设置和 ' + rules + ' 条域名规则。证书私钥不在备份中，恢复后端口如有变化页面可能短暂断开。', async () => {
        try {
          await api('/restore', { method: 'POST', body: data });
          toast('备份已恢复');
          await refreshAll();
        } catch (err) {
          toast(err.message, 'err');
        }
      }, '确认恢复');
    } catch (err) {
      toast('备份文件读取失败：' + err.message, 'err');
    }
    return;
  }
  if (t.name === 'redirect_mode') {
    const field = document.getElementById('redirect-port-field');
    if (field) field.style.display = t.value === 'custom_port' ? '' : 'none';
    return;
  }
});

document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-logkind]');
  if (b) {
    S.logKind = b.dataset.logkind;
    document.querySelectorAll('[data-logkind]').forEach((x) => x.classList.toggle('active', x === b));
    loadLogs();
    return;
  }
});

document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-theme-choice]');
  if (b) applyTheme(b.dataset.themeChoice);
});

document.addEventListener('input', (e) => {
  if (e.target.closest('form[data-form="domain-replace"]')) {
    updateDomainReplacePreview();
    return;
  }
  if (e.target.id === 'rule-search') {
    S.ruleQuery = e.target.value;
    const q = S.ruleQuery.toLowerCase();
    let shown = 0;
    document.querySelectorAll('#rule-grid .rule-card').forEach((card) => {
      const ok = !q || (card.dataset.search || '').includes(q);
      card.style.display = ok ? '' : 'none';
      if (ok) shown++;
    });
    const c = document.getElementById('rule-count');
    if (c) c.textContent = shown + ' / ' + (S.data.config.rules || []).length + ' 条';
    return;
  }
  if (e.target.id === 'log-host') {
    S.logHost = e.target.value.trim();
    clearTimeout(window.__logTimer);
    window.__logTimer = setTimeout(() => loadLogs(true), 300);
  }
});

document.addEventListener('submit', async (e) => {
  const f = e.target.closest('form[data-form]');
  if (!f) return;
  e.preventDefault();
  const kind = f.dataset.form;
  const fd = new FormData(f);
  try {
    if (kind === 'test') {
      S.testHost = String(fd.get('host') || '').trim();
      if (!S.testHost) return;
      S.testResult = await api('/test', { method: 'POST', body: { host: S.testHost } });
      render();
      return;
    }
    if (kind === 'settings') {
      const val = (n) => parseInt(fd.get(n), 10) || 0;
      const proxy = {
        port: val('port'),
        http_redirect: f.querySelector('[name=http_redirect]').checked,
        redirect_mode: (f.querySelector('input[name=redirect_mode]:checked') || {}).value || 'same_port',
        redirect_port: val('redirect_port'),
        extra_http_port: val('extra_http_port'),
        default_upstream: String(fd.get('default_upstream') || '').trim(),
        hsts: f.querySelector('[name=hsts]').checked,
        auto_fallback: false,
        title: S.data.config.proxy.title || 'FnRelay',
      };
      const certs = {
        auto_fnos: f.querySelector('[name=auto_fnos]').checked,
        extra_dirs: String(fd.get('extra_dirs') || '').split('\n').map((s) => s.trim()).filter(Boolean),
        default_cert: String(fd.get('default_cert') || ''),
      };
      const logs = {
        file_enabled: f.querySelector('[name=log_file_enabled]').checked,
        access: f.querySelector('[name=log_access]').checked,
        audit: f.querySelector('[name=log_audit]').checked,
        retention_days: val('log_retention'),
        max_file_mb: val('log_max_mb'),
      };
      try {
        await api('/settings', { method: 'POST', body: { proxy, certs, logs } });
        toast('设置已保存');
      } catch (err) {
        toast(err.message, 'err');
      }
      await refreshAll();
      return;
    }
  } catch (err) {
    toast(err.message, 'err');
  }
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closeAllSelects();
  if (e.key === 'Escape' && modalRoot.innerHTML) closeModal();
});

window.addEventListener('hashchange', async () => {
  if (!S.data || !S.data.authenticated) return;
  S.page = currentPage();
  if (modalRoot.innerHTML) closeModal();
  if (S.page === 'overview') {
    await refreshAll();
    return;
  }
  render(true);
  if (S.page === 'live') await loadLive(true);
  if (S.page === 'logs') await loadLogs(true);
});

setInterval(async () => {
  if (!S.data || !S.data.authenticated || document.hidden || modalRoot.innerHTML) return;
  if (S.page === 'logs') { if (S.autoLogs) await loadLogs(true); return; }
  if (S.page !== 'overview') return;
  try {
    const prev = S.data;
    const d = await api('/state');
    S.data = d;
    const st = d.status;
    document.querySelectorAll('[data-live]').forEach((el) => {
      const k = el.dataset.live;
      if (k === 'requests') el.textContent = fmtNum(st.requests);
      else if (k === 'uptime') el.textContent = fmtDuration(st.uptime);
      else if (k === 'rules') el.textContent = String(st.rule_count);
      else if (k === 'certs') el.textContent = String(st.cert_count);
    });
    await loadRecentAccess();
    if (!prev.status || prev.status.proxy_running !== st.proxy_running) render();
  } catch (err) { /* 静默 */ }
}, 8000);

setInterval(async () => {
  if (!S.data || !S.data.authenticated || document.hidden || modalRoot.innerHTML) return;
  if (S.page === 'live') {
    if (S.autoLive) await loadLive(true);
    return;
  }
  if (S.page === 'overview') {
    await loadLive(true);
    await loadStats(true);
  }
}, 1000);

applyTheme(savedTheme(), false);
if (window.matchMedia) {
  const mq = window.matchMedia('(prefers-color-scheme: dark)');
  const onChange = () => { if (savedTheme() === 'auto') applyTheme('auto', false); };
  if (mq.addEventListener) mq.addEventListener('change', onChange);
  else if (mq.addListener) mq.addListener(onChange);
}

boot();
