#!/usr/bin/env node
// Real Chromium smoke/regression test for comment batch UX.
// Setup (outside this repo):
//   npm install --prefix /tmp/gust-browser --no-save playwright-core
//   PLAYWRIGHT_CORE=/tmp/gust-browser/node_modules/playwright-core \
//     node tools/comment-batch-browser.mjs
// Chromium defaults to /home/pi/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome.
// The script builds Gust, starts an isolated Python fixture server through Gust
// (temporary HOME and project dir), and always stops/removes its test resources.
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import net from 'node:net';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const require = createRequire(import.meta.url);
const requirePaths = process.env.PLAYWRIGHT_CORE ? [process.env.PLAYWRIGHT_CORE] : [];
const { chromium } = require.resolve('playwright-core', { paths: requirePaths }).length
  ? require(require.resolve('playwright-core', { paths: requirePaths }))
  : {};
const chromePath = process.env.CHROMIUM || '/home/pi/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome';
const temp = await mkdtemp(path.join(tmpdir(), 'gust-batch-browser-'));
const fixture = path.join(temp, 'fixture');
const home = path.join(temp, 'home');
await mkdir(fixture); await mkdir(home);
await writeFile(path.join(fixture, 'index.html'), `<!doctype html><meta charset="utf-8"><title>Gust batch fixture</title>
<style>body{font:16px system-ui;margin:60px}#target{margin:30px;padding:40px;background:#eee;border:1px solid #aaa}input{padding:8px}</style>
<h1>Browser fixture</h1><label>Editable test <input id="editable" value=""></label>
<button id="target" data-testid="target">Commentable fixture target</button>
<button id="target2" data-testid="target2">Second comment target</button>`);

async function freePort() {
  const server = net.createServer();
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}
const appPort = await freePort();
let proxyPort = await freePort();
while (proxyPort === appPort) proxyPort = await freePort();
const bin = path.join(temp, 'gust');
let server;
let browser;
const logs = [];
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function eventually(fn, message, timeout = 8000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try { const result = await fn(); if (result) return result; } catch (e) { last = e; }
    await sleep(100);
  }
  throw new Error(message + (last ? `: ${last.message}` : ''));
}
const SHORTCUT_UPGRADE_MS = 350;
async function commentModeActive(page) {
  return (await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed')) === 'true';
}
async function batchModeActive(page) {
  return (await page.locator('html.__gust_batch_mode').count()) === 1;
}
// Batch mode is reached only through the Ctrl+C double-tap. When a mode is
// already active, a single Ctrl+C turns it off; wait past the upgrade window
// first so a single-shot press means "off" instead of "upgrade".
async function enableBatch(page) {
  if (await commentModeActive(page)) {
    await page.waitForTimeout(SHORTCUT_UPGRADE_MS + 100);
    await page.keyboard.press('Control+c');
    await eventually(() => page.locator('#__gust_comment_bubble').getAttribute('aria-pressed').then(v => v === 'false'), 'Ctrl+C did not turn comment mode off before enabling batch');
  }
  await page.keyboard.press('Control+c');
  await page.keyboard.press('Control+c');
  await eventually(() => page.locator('html.__gust_batch_mode').count().then(n => n === 1), 'double Ctrl+C did not enable batch mode');
}
// A single Ctrl+C turns batch mode off.
async function disableBatch(page) {
  if (!(await batchModeActive(page))) return;
  await page.keyboard.press('Control+c');
  await eventually(() => page.locator('#__gust_comment_bubble').getAttribute('aria-pressed').then(v => v === 'false'), 'Ctrl+C did not turn batch mode off');
}
async function api(page, endpoint, method = 'GET', body) {
  return page.evaluate(async ({ endpoint, method, body }) => {
    const response = await fetch(endpoint, { method, headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined });
    const data = await response.json().catch(() => null);
    if (!response.ok) throw new Error(`${method} ${endpoint} => ${response.status}: ${JSON.stringify(data)}`);
    return data;
  }, { endpoint, method, body });
}
async function comments(page) { return api(page, '/__gust/comments'); }
let targetClickIndex = 0;
async function clickFixtureTarget(page) {
  const index = targetClickIndex++;
  await page.locator('#target').click({ position: { x: 30 + (index % 6) * 32, y: 25 + Math.floor(index / 6) * 30 } });
}
async function waitDraft(page, text) {
  return eventually(async () => (await comments(page)).find(c => c.text === text && c.state === 'created'), `draft not saved: ${text}`);
}
async function createDraft(page, text, { exclude = false, sendNow = false } = {}) {
  const selecting = await page.locator('html').evaluate(el => el.classList.contains('__gust_selecting'));
  if (!selecting) await page.locator('#__gust_comment_bubble').click();
  await eventually(() => page.locator('html.__gust_selecting').count().then(n => n === 1), 'comment selection did not activate');
  await clickFixtureTarget(page);
  const editor = page.locator('[data-editor]');
  if (await page.locator('html').evaluate(el => el.classList.contains('__gust_batch_mode'))) {
    for (const selector of ['[data-exclude-label]', '[data-save-draft]', '[data-send-now]'])
      assert.equal(await editor.locator(selector).isVisible(), true, `${selector} must be visible in batch editor`);
    assert.equal(await editor.locator('[data-save]').isVisible(), false, 'normal Save comment action must not replace batch actions');
  }
  await editor.locator('textarea').fill(text);
  if (exclude) await editor.locator('[data-exclude-label] input').check();
  if (sendNow) await editor.locator('[data-send-now]').click();
  else if (await editor.locator('[data-save-draft]').isVisible()) await editor.locator('[data-save-draft]').click();
  else await editor.locator('[data-save]').click();
  if (sendNow) return eventually(async () => (await comments(page)).find(c => c.text === text && c.state !== 'created'), `single comment was not submitted: ${text}`);
  return waitDraft(page, text);
}
async function assertDraftSet(page, expected) {
  const current = (await comments(page)).filter(c => c.state === 'created').map(c => c.text).sort();
  assert.deepEqual(current, expected.slice().sort(), 'draft set/membership scope');
}

try {
  execFileSync('go', ['build', '-o', bin, './cmd/gust'], { cwd: root, stdio: 'inherit' });
  const shellQuote = value => `'${String(value).replaceAll("'", "'\\''")}'`;
  const exec = `python3 -m http.server ${appPort} --bind 127.0.0.1 --directory ${shellQuote(fixture)}`;
  server = spawn(bin, ['-e', exec, '-p', `${appPort}:${proxyPort}`, '--optin', 'comments'], {
    cwd: fixture, env: { ...process.env, HOME: home, XDG_CONFIG_HOME: path.join(home, '.config'), XDG_STATE_HOME: path.join(home, '.local', 'state') },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  server.stdout.on('data', b => logs.push(String(b)));
  server.stderr.on('data', b => logs.push(String(b)));
  const base = `http://127.0.0.1:${proxyPort}`;
  await eventually(async () => { const r = await fetch(base); return r.ok; }, 'isolated Gust proxy did not start', 30000);
  browser = await chromium.launch({ headless: true, executablePath: chromePath, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  const context = await browser.newContext();
  const page = await context.newPage();
  const pageErrors = [];
  page.on('pageerror', e => pageErrors.push(e.message));
  await page.goto(base, { waitUntil: 'networkidle' });
  await eventually(() => page.locator('#__gust_comment_bubble').count().then(n => n === 1), 'comments toolbar did not mount');

  // Ctrl+B was removed. It must never activate or change comment mode.
  await page.locator('body').click({ position: { x: 700, y: 30 } });
  await page.keyboard.press('Control+b');
  await page.waitForTimeout(100);
  assert.equal(await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed'), 'false', 'Ctrl+B must not activate comment mode');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'Ctrl+B must not enable batch mode');

  // Ctrl+C from off enters single-shot mode immediately, not batch.
  await page.keyboard.press('Control+c');
  assert.equal(await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed'), 'true', 'Ctrl+C must enter single-shot comment mode');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'a single Ctrl+C must not enable batch mode');
  // Ctrl+B cannot upgrade the active single-shot mode.
  await page.keyboard.press('Control+b');
  await page.waitForTimeout(100);
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'Ctrl+B must not upgrade single-shot to batch');
  assert.equal(await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed'), 'true', 'Ctrl+B must leave single-shot active');
  // Once the 350ms upgrade window has passed, Ctrl+C turns single-shot off.
  await page.waitForTimeout(SHORTCUT_UPGRADE_MS + 100);
  await page.keyboard.press('Control+c');
  await eventually(() => page.locator('#__gust_comment_bubble').getAttribute('aria-pressed').then(v => v === 'false'), 'Ctrl+C after the upgrade window must turn single-shot off');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'turning single-shot off must not enable batch');

  // A second Ctrl+C within the 350ms window upgrades the gesture to batch.
  await page.keyboard.press('Control+c');
  await page.keyboard.press('Control+c');
  await eventually(() => page.locator('html.__gust_batch_mode').count().then(n => n === 1), 'double Ctrl+C did not upgrade to batch mode');
  assert.equal(await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed'), 'true', 'batch upgrade must keep comment selection active');
  const blue = await eventually(async () => {
    const color = await page.locator('#__gust_comment_bubble').evaluate(el => getComputedStyle(el).color);
    return /rgb\(96, 165, 250\)|rgb\(59, 130, 246\)/.test(color) ? color : false;
  }, 'batch mode blue transition did not settle');
  assert.match(blue, /rgb\(96, 165, 250\)|rgb\(59, 130, 246\)/);
  // Ctrl+B cannot take batch mode back off.
  await page.keyboard.press('Control+b');
  await page.waitForTimeout(100);
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 1, 'Ctrl+B must not disable batch mode');
  assert.equal(await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed'), 'true', 'Ctrl+B must not disable batch selection');
  // A single Ctrl+C turns batch mode off.
  await page.keyboard.press('Control+c');
  await eventually(() => page.locator('#__gust_comment_bubble').getAttribute('aria-pressed').then(v => v === 'false'), 'Ctrl+C in batch mode must turn comment mode off');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'Ctrl+C in batch mode must clear batch mode');

  // Editable fields and selected text keep normal copy behavior.
  await page.locator('#editable').focus();
  await page.locator('#editable').fill('editable copy text');
  await page.locator('#editable').evaluate(el => el.setSelectionRange(0, el.value.length));
  await page.keyboard.press('Control+c');
  await page.waitForTimeout(100);
  assert.equal(await page.locator('#editable').inputValue(), 'editable copy text', 'Ctrl+C in an editable must not change its value');
  assert.equal(await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed'), 'false', 'Ctrl+C in an editable must not activate comment mode');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'Ctrl+C in an editable must not enable batch mode');
  await page.locator('body').click({ position: { x: 700, y: 30 } });
  await page.locator('#target').evaluate(el => { const range = document.createRange(); range.selectNodeContents(el); const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range); });
  await page.keyboard.press('Control+c');
  await page.waitForTimeout(100);
  assert.equal(await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed'), 'false', 'Ctrl+C with selected text must not activate comment mode');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'Ctrl+C with selected text must not enable batch mode');

  // Create a pre-existing standalone draft with autosubmit disabled.
  if (await page.locator('#__gust_comment_bubble').getAttribute('aria-pressed') !== 'true') await page.locator('#__gust_comment_bubble').click();
  // The editor is portaled outside the widget; its toolbar checkbox can be
  // visually hidden when the pointer leaves the floating UI, so set its actual
  // DOM state directly for this setup-only preference.
  await page.locator('#__gust_comments [data-autosubmit]').evaluate(el => { el.checked = false; el.dispatchEvent(new Event('change', { bubbles: true })); });
  await page.locator('#target').click();
  assert.equal(await page.locator('[data-editor] [data-save]').isVisible(), true, 'normal draft save action should be visible');
  assert.equal(await page.locator('[data-editor] [data-save-draft]').isVisible(), false, 'batch actions should be hidden outside batch mode');
  await page.locator('[data-editor] textarea').fill('preexisting normal draft');
  await page.locator('[data-editor] [data-save]').click();
  const normal = await waitDraft(page, 'preexisting normal draft');
  assert.equal(normal.inBatch, false, 'normal draft unexpectedly joined batch');

  await enableBatch(page);
  const first = await createDraft(page, 'first batch member');
  assert.equal(first.inBatch, true, 'new batch-mode draft must be included');
  const excluded = await createDraft(page, 'excluded note', { exclude: true });
  assert.equal(excluded.inBatch, false, 'excluded note must remain standalone');
  const second = await createDraft(page, 'second batch member');
  assert.equal(second.inBatch, true, 'repeated save in batch mode must join same batch');
  await assertDraftSet(page, ['preexisting normal draft', 'first batch member', 'excluded note', 'second batch member']);
  await disableBatch(page);
  assert.equal(await page.locator('#__gust_batch_send').isVisible(), true, 'batch send count must persist with mode off');
  assert.match(await page.locator('#__gust_batch_send').innerText(), /2 comments in draft/);
  const sendBox = await page.locator('#__gust_batch_send').boundingBox();
  const bubbleBox = await page.locator('#__gust_comment_bubble').boundingBox();
  assert.ok(sendBox && bubbleBox && sendBox.x + sendBox.width <= bubbleBox.x, 'persistent Send control should sit left of the floating comments bubble');
  await page.reload({ waitUntil: 'networkidle' });
  await eventually(() => page.locator('#__gust_comment_bubble').count().then(n => n === 1), 'reload did not remount UI');
  await assertDraftSet(page, ['preexisting normal draft', 'first batch member', 'excluded note', 'second batch member']);
  assert.equal((await comments(page)).find(c => c.text === 'first batch member').inBatch, true, 'membership did not survive reload');
  assert.equal((await comments(page)).find(c => c.text === 'second batch member').inBatch, true, 'repeated membership did not survive reload');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'inactive mode should remain inactive on reload');

  // Open the normal comments panel with batch mode off, then join a draft.
  await page.locator('#__gust_comment_bubble').click();
  await page.locator('#__gust_icon').hover();
  await page.locator('[data-comment-id="' + normal.id + '"] .__gust_comment_row').click();
  await page.locator('#__gust_comment_popover .__gust_popover_action').filter({ hasText: 'Add to the batch' }).click();
  await eventually(async () => (await comments(page)).find(c => c.id === normal.id)?.inBatch === true, 'existing draft did not join batch');
  await page.locator('#__gust_comment_popover [aria-label="Close comment"]').click().catch(() => {});
  await assertDraftSet(page, ['preexisting normal draft', 'first batch member', 'excluded note', 'second batch member']);

  // Turning collection mode on resumes membership. Lose one committed create
  // response; retry Save draft with the same immutable payload/client ID.
  await enableBatch(page);
  const originalCreatePayloads = [];
  let loseCreateResponse = true;
  await page.route('**/__gust/comments', async route => {
    if (route.request().method() !== 'POST') return route.continue();
    originalCreatePayloads.push(route.request().postDataJSON());
    if (loseCreateResponse) {
      loseCreateResponse = false;
      const response = await route.fetch();
      assert.equal(response.status(), 201, 'first create should commit before simulating a lost response');
      await response.dispose();
      await route.abort();
      return;
    }
    return route.continue();
  });
  const selectingForLostCreate = await page.locator('html').evaluate(el => el.classList.contains('__gust_selecting'));
  if (!selectingForLostCreate) await page.locator('#__gust_comment_bubble').click();
  await clickFixtureTarget(page);
  const createEditor = page.locator('[data-editor]');
  await createEditor.locator('textarea').fill('lost create response');
  await createEditor.locator('[data-save-draft]').click();
  await eventually(async () => /outcome unclear|failed|NetworkError|fetch/i.test(await createEditor.locator('[data-result]').innerText()), 'lost create response did not leave a retryable error');
  assert.equal(await createEditor.locator('textarea').isEditable(), false, 'uncertain create must lock payload until safe retry');
  await createEditor.locator('[data-save-draft]').click();
  const replayedCreate = await waitDraft(page, 'lost create response');
  assert.equal(replayedCreate.inBatch, true, 'retried create lost original batch membership');
  assert.equal(await comments(page).then(cs => cs.filter(c => c.text === 'lost create response').length), 1, 'lost create response replay created a duplicate draft');
  assert.equal(originalCreatePayloads.length, 2, 'expected exactly one create retry');
  assert.deepEqual(originalCreatePayloads[1], originalCreatePayloads[0], 'create retry payload/client ID changed');
  await page.unroute('**/__gust/comments');

  // A UTF-8 byte-limit 400 is definitive and leaves the editor editable.
  const validationText = '🧪'.repeat(2049); // 4-byte UTF-8 code points, under textarea maxlength.
  await clickFixtureTarget(page);
  const validationEditor = page.locator('[data-editor]');
  await validationEditor.locator('textarea').fill(validationText);
  assert.ok((await validationEditor.locator('textarea').inputValue()).length < 8192, 'invalid fixture must fit textarea maxlength');
  await validationEditor.locator('[data-save-draft]').click();
  await eventually(async () => /Could not save:.*8192 bytes/.test(await validationEditor.locator('[data-result]').innerText()), 'server 400 did not expose recoverable validation error');
  assert.equal(await validationEditor.locator('textarea').isEditable(), true, 'definitive 400 should restore editor for correction');
  await validationEditor.locator('textarea').fill('recovered after validation');
  await validationEditor.locator('[data-save-draft]').click();
  const recovered = await waitDraft(page, 'recovered after validation');
  assert.equal(recovered.inBatch, true, 'corrected draft lost batch membership');

  // A lost Send now create response retried with Ctrl+Enter must save, not send.
  const sendCreatePayloads = [];
  let loseSendCreateResponse = true;
  await page.route('**/__gust/comments', async route => {
    if (route.request().method() !== 'POST') return route.continue();
    sendCreatePayloads.push(route.request().postDataJSON());
    if (loseSendCreateResponse) {
      loseSendCreateResponse = false;
      const response = await route.fetch();
      assert.equal(response.status(), 201, 'Send now create should commit before response loss');
      await response.dispose();
      await route.abort();
      return;
    }
    return route.continue();
  });
  await clickFixtureTarget(page);
  const retrySendEditor = page.locator('[data-editor]');
  await retrySendEditor.locator('textarea').fill('lost Send now create');
  await retrySendEditor.locator('[data-send-now]').click();
  await eventually(async () => /Save outcome unclear/i.test(await retrySendEditor.locator('[data-result]').innerText()), 'lost Send now create did not keep a retryable draft');
  await retrySendEditor.locator('textarea').press('Control+Enter');
  const rescuedSendDraft = await waitDraft(page, 'lost Send now create');
  assert.equal(rescuedSendDraft.inBatch, false, 'Ctrl+Enter retry should preserve Send now standalone membership');
  assert.equal(rescuedSendDraft.state, 'created', 'Ctrl+Enter retry unexpectedly sent the draft');
  assert.equal(sendCreatePayloads.length, 2, 'expected one Send now create retry');
  assert.deepEqual(sendCreatePayloads[1], sendCreatePayloads[0], 'Send now create retry changed immutable payload/client ID');
  assert.equal(await comments(page).then(cs => cs.filter(c => c.text === 'lost Send now create').length), 1, 'Send now create retry duplicated draft');
  await page.unroute('**/__gust/comments');

  // Force one non-committed Send now failure, then retry the retained draft.
  let failFirstSubmit = true;
  await page.route('**/__gust/comments/submit?id=*', async route => {
    if (failFirstSubmit) { failFirstSubmit = false; await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { message: 'injected send failure' } }) }); }
    else await route.continue();
  });
  const selectingForSend = await page.locator('html').evaluate(el => el.classList.contains('__gust_selecting'));
  if (!selectingForSend) await page.locator('#__gust_comment_bubble').click();
  await clickFixtureTarget(page);
  const sendEditor = page.locator('[data-editor]');
  await sendEditor.locator('textarea').fill('send now standalone');
  await sendEditor.locator('[data-send-now]').click();
  let singleDraft = await waitDraft(page, 'send now standalone');
  assert.equal(singleDraft.inBatch, false, 'Send now draft joined pending batch');
  await eventually(async () => /Send failed.*retry Send now/.test(await sendEditor.locator('[data-result]').innerText()), 'failed Send now did not expose retry action');
  assert.equal(await comments(page).then(cs => cs.filter(c => c.text === 'send now standalone').length), 1, 'failed Send now created duplicates');
  await sendEditor.locator('[data-send-now]').click();
  const single = await eventually(async () => (await comments(page)).find(c => c.text === 'send now standalone' && c.state !== 'created'), 'Send now retry did not submit saved draft');
  assert.equal(single.state, 'submitted');
  assert.equal(await comments(page).then(cs => cs.filter(c => c.text === 'send now standalone').length), 1, 'Send now retry created a duplicate');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 1, 'Send now changed mode');
  await page.unroute('**/__gust/comments/submit?id=*');

  // Lose a successful Send now response. Retrying must recognize the committed
  // thread, close the editor, and never create or submit a second comment.
  let loseSubmitResponse = true;
  await page.route('**/__gust/comments/submit?id=*', async route => {
    if (!loseSubmitResponse) return route.continue();
    loseSubmitResponse = false;
    const response = await route.fetch();
    assert.equal(response.status(), 200, 'Send now should commit before simulating a lost response');
    await response.dispose();
    await route.abort();
  });
  const selectingForLostSubmit = await page.locator('html').evaluate(el => el.classList.contains('__gust_selecting'));
  if (!selectingForLostSubmit) await page.locator('#__gust_comment_bubble').click();
  await clickFixtureTarget(page);
  const lostSubmitEditor = page.locator('[data-editor]');
  await lostSubmitEditor.locator('textarea').fill('lost submit response');
  await lostSubmitEditor.locator('[data-send-now]').click();
  const committed = await eventually(async () => (await comments(page)).find(c => c.text === 'lost submit response' && c.state === 'submitted'), 'submit endpoint did not commit before response loss');
  assert.match(await lostSubmitEditor.locator('[data-result]').innerText(), /Send failed/i, 'lost response should leave retry affordance visible');
  await lostSubmitEditor.locator('[data-send-now]').click();
  await eventually(() => lostSubmitEditor.isHidden(), 'retry did not treat already-submitted thread as success');
  assert.equal(await comments(page).then(cs => cs.filter(c => c.text === 'lost submit response').length), 1, 'lost submit response retry created a duplicate');
  assert.equal((await comments(page)).find(c => c.id === committed.id).state, 'submitted');
  await page.unroute('**/__gust/comments/submit?id=*');
  await assertDraftSet(page, ['preexisting normal draft', 'first batch member', 'excluded note', 'second batch member', 'lost create response', 'recovered after validation', 'lost Send now create']);
  await disableBatch(page);

  // Batch send only submits opted-in members, leaving excluded drafts untouched.
  const submitBatch = page.locator('#__gust_batch_send');
  await submitBatch.click();
  await eventually(async () => (await comments(page)).filter(c => c.state === 'submitted').length === 7, 'two standalone sends + five batch members were not submitted');
  let data = await comments(page);
  const submitted = data.filter(c => c.state === 'submitted');
  const standaloneTexts = ['send now standalone', 'lost submit response', 'lost Send now create'];
  const batchIDs = new Set(submitted.filter(c => !standaloneTexts.includes(c.text)).map(c => c.batchId));
  assert.equal(batchIDs.size, 1, 'batch members should share a submitted batch ID');
  assert.deepEqual(data.filter(c => c.state === 'created').map(c => c.text).sort(), ['excluded note', 'lost Send now create'].sort(), 'batch send submitted a nonmember');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 0, 'batch send changed mode');

  // Submitted batch grouping shows progress; a reply/review and resolution remain per-thread.
  const groupedID = [...batchIDs][0];
  const members = submitted.filter(c => c.batchId === groupedID);
  const group = page.locator('.__gust_batch_group').filter({ hasText: members[1].text });
  await eventually(() => group.count().then(n => n === 1), `five-member submitted batch group missing; comments=${JSON.stringify(data.map(c => ({ text:c.text, state:c.state, batchId:c.batchId })))}`);
  assert.match(await group.textContent(), /0 of 5 ready for review/);
  await api(page, `/__gust/comments/${members[0].id}/reply`, 'POST', { text: 'agent response' });
  // Human replies and resolution still advance only their own thread.
  await eventually(async () => (await comments(page)).find(c => c.id === members[0].id)?.messages?.length > 0, 'reply was not retained');
  await page.locator('#__gust_comment_bubble').click();
  await page.locator('#__gust_icon').hover();
  await page.locator(`[data-comment-id="${members[0].id}"] .__gust_comment_row`).click();
  await page.locator('#__gust_comment_popover .__gust_popover_resolve').click();
  await eventually(async () => (await comments(page)).find(c => c.id === members[0].id)?.state === 'done', 'UI thread resolution did not persist');
  await page.waitForTimeout(3200);
  data = await comments(page);
  assert.equal(data.find(c => c.id === members[1].id).state, 'submitted', 'resolving one thread affected another');
  assert.match(await group.textContent(), /1 of 5 ready for review/, 'batch progress should include resolved thread');

  // Send another collection while batch mode and element selection are active.
  // The floating Send action must not be captured as a page comment target.
  await enableBatch(page);
  const activeModeDraft = await createDraft(page, 'sent while mode active');
  assert.equal(activeModeDraft.inBatch, true);
  assert.equal(await page.locator('html.__gust_selecting').count(), 1, 'Save draft should leave selection active');
  await page.locator('#__gust_batch_send').click();
  const secondBatchMember = await eventually(async () => (await comments(page)).find(c => c.text === 'sent while mode active' && c.state === 'submitted'), 'Send while mode active was intercepted or failed');
  assert.equal(await page.locator('html.__gust_batch_mode').count(), 1, 'active-mode batch Send disabled batch mode');
  assert.equal(await comments(page).then(cs => cs.find(c => c.text === 'excluded note')?.state), 'created', 'second batch send submitted excluded note');
  assert.ok(secondBatchMember.batchId && secondBatchMember.batchId !== groupedID, 'second Send should create a distinct submitted batch');
  await eventually(() => page.locator('.__gust_batch_group').filter({ hasText: 'sent while mode active' }).count().then(n => n === 1), 'second submitted batch group missing');
  assert.ok(pageErrors.length === 0, `page errors: ${pageErrors.join('; ')}`);
  console.log('PASS: headless Chromium comment batch UX');
} catch (error) {
  console.error('FAIL:', error);
  if (logs.length) console.error('Gust server log:\n' + logs.join(''));
  process.exitCode = 1;
} finally {
  if (browser) await browser.close().catch(() => {});
  if (server && server.exitCode === null) { server.kill('SIGTERM'); await Promise.race([new Promise(resolve => server.once('exit', resolve)), sleep(3000)]); if (server.exitCode === null) server.kill('SIGKILL'); }
  await rm(temp, { recursive: true, force: true });
}
