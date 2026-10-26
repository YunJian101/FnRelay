const fs = require('fs');
const vm = require('vm');
const path = require('path');

const web = path.join(__dirname, '..', 'src', 'web');
const fixture = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'build', 'state.json'), 'utf8'));
let code = fs.readFileSync(path.join(web, 'app.js'), 'utf8');
code += '\nglobalThis.__S = S; globalThis.__render = render; globalThis.__boot = boot;';

const rootEl = { innerHTML: '', style: {}, classList: { add() {}, remove() {}, toggle() {} } };
const generic = () => ({
  innerHTML: '', style: {}, classList: { add() {}, remove() {}, toggle() {} },
  appendChild() {}, querySelector() { return null; }, querySelectorAll() { return []; },
  addEventListener() {}, remove() {}, dataset: {}, setAttribute() {},
});
const document = {
  baseURI: 'http://127.0.0.1:18899/',
  getElementById(id) { return id === 'root' ? rootEl : generic(); },
  querySelector() { return null; },
  querySelectorAll() { return []; },
  addEventListener() {},
  createElement() { return generic(); },
  body: { style: {} },
  documentElement: { dataset: {}, style: {} },
  hidden: false,
};
const ctx = {
  console,
  document,
  window: { scrollY: 0, scrollTo() {}, addEventListener() {} },
  history: { replaceState() {} },
  location: { hash: '' },
  fetch: async () => ({ ok: true, status: 200, json: async () => fixture }),
  setInterval: () => 0,
  clearTimeout: () => 0,
  setTimeout: () => 0,
  Intl,
  URLSearchParams,
  URL,
  FormData: class {},
  navigator: { clipboard: { writeText: async () => {} } },
};
ctx.globalThis = ctx;
vm.createContext(ctx);
vm.runInContext(code, ctx, { filename: 'app.js' });

(async () => {
  await ctx.__boot();
  console.log('authenticated:', ctx.__S.data.authenticated, 'root html:', rootEl.innerHTML.length);
  for (const page of ['overview', 'live', 'rules', 'certs', 'logs', 'settings']) {
    ctx.__S.page = page;
    ctx.__render(true);
    const html = rootEl.innerHTML;
    if (!html || html.length < 500) throw new Error('page too small: ' + page);
    if (html.includes('undefined')) throw new Error('page contains undefined: ' + page);
    console.log(page.padEnd(9), 'html length', html.length);
  }
  console.log('render check OK');
})().catch((e) => { console.error(e); process.exit(1); });

