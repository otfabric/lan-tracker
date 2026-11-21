// SPDX-License-Identifier: MIT
//
// Makes the README's demo video (docs/media/demo.mp4, demo.gif)
// and screenshots (docs/images). It runs against the demo LAN of
// docs/media/demo/demo-net.sh: a Docker bridge network with PLCs, drives, an
// HMI, a camera, a DHCP and DNS server and an engineering laptop, watched by
// lan-tracker in a container with only CAP_NET_RAW. The terminal scenes run the
// real CLI in that container (docker exec) and show its output as it came; the
// UI scenes drive the web UI in the local Chrome.
//
//   make build
//   docs/media/demo/demo-net.sh down; docs/media/demo/demo-net.sh up
//   sleep 150                                          # first ARP, ICMP, TCP and name passes
//   docs/media/demo/demo-net.sh cli ui url | head -1 > $SCRATCH/url.txt
//   cp docs/media/record-demo.mjs $SCRATCH && cd $SCRATCH && npm i playwright   # not in the repo
//   node record-demo.mjs video out/vid url.txt $REPO    # frames, demo.mp4 (captions in the picture), demo.gif
//   node record-demo.mjs shots out/png url.txt $REPO    # after the video: 2x screenshots -> docs/images
//   pngquant --skip-if-larger --ext .png --force out/png/*.png
//
// Video mode adds the late drive and the conflicting laptop to the LAN
// (demo-net.sh late, conflict) and stops and restarts active discovery; start
// with a fresh LAN for each video. Shots mode only reads.
import { chromium } from 'playwright';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { join, resolve } from 'node:path';

const mode = process.argv[2] ?? 'shots';
const out = resolve(process.argv[3] ?? `out/${mode}`);
const url = readFileSync(process.argv[4] ?? 'url.txt', 'utf8').trim();
const repo = resolve(process.argv[5] ?? '.');
const netScript = join(repo, 'docs/media/demo/demo-net.sh');
mkdirSync(out, { recursive: true });

const video = mode === 'video';
const browser = await chromium.launch({ channel: 'chrome', headless: true });
const context = await browser.newContext({
  viewport: video ? { width: 1280, height: 760 } : { width: 1440, height: 860 },
  deviceScaleFactor: video ? 1 : 2,
  colorScheme: 'dark',
});
// A visible pointer, the caption box and the terminal window, in the OT
// Fabric tokens of the page.
await context.addInitScript((isVideo) => {
  addEventListener('DOMContentLoaded', () => {
    if (isVideo) {
      const dot = document.createElement('div');
      dot.style.cssText =
        'position:fixed;z-index:2147483647;width:18px;height:18px;margin:-9px 0 0 -9px;border-radius:50%;' +
        'background:rgba(63,193,198,.35);border:2px solid #3fc1c6;pointer-events:none;transition:transform .08s;left:-40px;top:-40px';
      document.body.appendChild(dot);
      addEventListener('mousemove', (e) => { dot.style.left = e.clientX + 'px'; dot.style.top = e.clientY + 'px'; }, true);
      addEventListener('mousedown', () => { dot.style.transform = 'scale(.7)'; }, true);
      addEventListener('mouseup', () => { dot.style.transform = ''; }, true);
      const cap = document.createElement('div');
      cap.id = 'demo-caption';
      cap.style.cssText =
        'position:fixed;z-index:2147483646;left:50%;bottom:18px;transform:translateX(-50%);max-width:880px;width:max-content;' +
        'padding:6px 14px;border-radius:6px;border:1px solid color-mix(in srgb, var(--ot-accent) 60%, transparent);' +
        'background:rgba(9,15,19,.86);color:#e8f1f2;font:500 15px/1.4 var(--ot-font-body);text-align:center;' +
        'pointer-events:none;opacity:0;transition:opacity .3s';
      document.body.appendChild(cap);
    }
    const term = document.createElement('div');
    term.id = 'demo-term';
    term.style.cssText =
      'position:fixed;z-index:2147483640;inset:0;display:none;align-items:flex-start;justify-content:center;padding:28px 0 0;' +
      'background:color-mix(in srgb, var(--ot-bg) 72%, transparent);backdrop-filter:blur(3px)';
    term.innerHTML =
      '<div data-s="width:1180px;border:1px solid var(--ot-line);border-radius:8px;overflow:hidden;box-shadow:0 12px 40px rgba(0,0,0,.5)">' +
      '<div data-s="display:flex;gap:7px;align-items:center;padding:9px 12px;background:var(--ot-header-bg);border-bottom:1px solid var(--ot-line)">' +
      '<span data-s="width:11px;height:11px;border-radius:50%;background:#ff5f57"></span><span data-s="width:11px;height:11px;border-radius:50%;background:#febc2e"></span>' +
      '<span data-s="width:11px;height:11px;border-radius:50%;background:#28c840"></span>' +
      '<span data-s="margin-left:10px;font:500 13px var(--ot-font-body);color:var(--ot-muted)">ssh edge-box: the lan-tracker CLI</span></div>' +
      '<pre id="demo-term-text" data-s="margin:0;height:520px;overflow:hidden;padding:14px 16px;background:#0b0f14;color:#d6dde6;' +
      'font:13px/1.5 var(--ot-font-mono);white-space:pre-wrap;overflow-wrap:anywhere"></pre></div>';
    // The UI's Content-Security-Policy drops inline style attributes;
    // styles set through the CSSOM apply, so data-s becomes style.cssText.
    window.__styled = (root) => root.querySelectorAll('[data-s]').forEach((e) => { e.style.cssText = e.dataset.s; e.removeAttribute('data-s'); });
    window.__styled(term);
    document.body.appendChild(term);
  });
}, video);
const page = await context.newPage();

const frames = [];
let cdp;
if (video) {
  cdp = await context.newCDPSession(page);
  cdp.on('Page.screencastFrame', async (f) => {
    frames.push({ t: f.metadata.timestamp, data: f.data });
    await cdp.send('Page.screencastFrameAck', { sessionId: f.sessionId }).catch(() => {});
  });
}
const pause = (ms) => page.waitForTimeout(video ? ms : Math.min(ms, 600));
const shot = async (name) => {
  if (video) return;
  await page.mouse.move(2, 2);
  await page.waitForTimeout(900);
  await page.screenshot({ path: join(out, `${name}.png`) });
  console.log('shot', name);
};
let failures = 0;
const step = async (name, fn) => {
  try {
    await fn();
  } catch (e) {
    failures++;
    console.log('STEP FAILED', name, String(e).split('\n')[0]);
    await page.screenshot({ path: join(out, `failed-${name.replace(/ /g, '-')}.png`) }).catch(() => {});
  }
};

const now = async () => (video ? (await cdp.send('Runtime.evaluate', { expression: 'Date.now()', returnByValue: true })).result.value / 1000 : 0);
const caption = async (html) => {
  if (!video) return;
  await page.evaluate((h) => {
    const cap = document.getElementById('demo-caption');
    if (!cap) return;
    if (h) cap.innerHTML = h;
    cap.style.opacity = h ? '1' : '0';
  }, html);
};
const sections = [];
const highlight = async (seconds) => { if (video) sections.push({ start: await now(), seconds }); };

const click = async (locator) => {
  await locator.scrollIntoViewIfNeeded();
  const box = await locator.boundingBox();
  if (box) {
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2, { steps: video ? 14 : 1 });
    await pause(180);
  }
  await locator.click();
};
const typeInto = async (locator, text) => {
  await click(locator);
  await locator.fill('');
  await locator.pressSequentially(text, { delay: video ? 45 : 0 });
};
const nav = async (name) => {
  await click(page.getByRole('link', { name, exact: true }));
  await page.waitForTimeout(900);
};

// The demo LAN and the CLI in its edge container.
const net = (...args) => {
  const r = spawnSync(netScript, args, { encoding: 'utf8' });
  if (r.status !== 0) throw new Error(`demo-net.sh ${args.join(' ')}: ${r.stderr}`);
  return r.stdout;
};
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const prompt = '<span data-s="color:#3fc1c6">root@edge-box</span>:<span data-s="color:#7aa2f7">~</span># ';
const term = async (show) => {
  await page.evaluate((s) => {
    const t = document.getElementById('demo-term');
    t.style.display = s ? 'flex' : 'none';
    if (s) document.getElementById('demo-term-text').innerHTML = '';
  }, show);
};
const termAppend = (html) => page.evaluate((h) => {
  const el = document.getElementById('demo-term-text');
  el.insertAdjacentHTML('beforeend', h);
  window.__styled(el);
  el.scrollTop = el.scrollHeight;
}, html);
// Types a command, runs it in the edge container, prints its real output line by line.
const cli = async (args, readMs = 4000) => {
  const line = 'lan-tracker ' + args.map((a) => (/[\s"]/.test(a) ? `"${a}"` : a)).join(' ');
  await termAppend(prompt);
  for (const ch of line) {
    await termAppend(esc(ch));
    await page.waitForTimeout(video ? 28 : 0);
  }
  await page.waitForTimeout(video ? 350 : 0);
  await termAppend('\n');
  const r = spawnSync('docker', ['exec', '-e', 'COLUMNS=118', 'lt-edge', 'lan-tracker', '--no-color', ...args], { encoding: 'utf8' });
  const text = (r.stdout + r.stderr).replace(/\x1b\[[0-9;]*m/g, '').replace(/\s+$/, '');
  for (const l of text.split('\n')) {
    let h = esc(l);
    h = h.replace(/\b(CONFLICT|DUPLICATE_IP_DETECTED|disabled \(.*?\)|REFUSED)\b/g, '<span data-s="color:#f7768e">$1</span>');
    h = h.replace(/\b(OPEN|ALLOWED|ACTIVE|OK|enabled)\b/g, '<span data-s="color:#9ece6a">$1</span>');
    await termAppend(h + '\n');
    await page.waitForTimeout(video ? 25 : 0);
  }
  await termAppend('\n');
  await pause(readMs);
  return text;
};

await page.goto(url);
await page.getByRole('link', { name: 'Devices', exact: true }).waitFor({ timeout: 30000 });
await page.waitForTimeout(2500);
if (await page.getByText('Open the UI with its token').count()) {
  console.log('the daemon rejected the token: write url.txt again (demo-net.sh cli ui url)');
  process.exit(2);
}
await page.getByRole('radio', { name: '1h', exact: true }).click();
await page.waitForTimeout(1500);
if (video) {
  await cdp.send('Page.startScreencast', { format: 'png', maxWidth: 1280, maxHeight: 760, everyNthFrame: 1 });
  await page.waitForTimeout(300);
}

if (video) {
  // 1. Title, on the overview.
  await highlight(7);
  await caption('<b>LAN Tracker</b>: the history of every device on an OT network segment.<br>Passive listening first; active discovery only where you allow it, within strict rates.');
  await pause(8000);

  // 2. The CLI on the edge box.
  await step('status', async () => {
    await term(true);
    await caption('On the edge box, one daemon watches <b>eth0</b>: packet capture, the kernel neighbour table, and bounded ARP, ICMP, TCP and name probes.');
    await cli(['daemon', 'status'], 3500);
    await caption('<b>daemon status</b> shows every collector, the last pass of each probe, and whether the web UI is on (here: all addresses, port 9735).');
    await pause(5500);
  });
  await step('hosts list', async () => {
    await highlight(8);
    await term(true);
    await caption('The inventory: one host per MAC and interface, its addresses, names from mDNS, DHCP and DNS, the IEEE registrant and its presence.');
    await cli(['hosts', 'list'], 8000);
    await term(false);
  });

  // 3. The UI.
  await step('overview', async () => {
    await caption('The same data in the browser: <b>lan-tracker ui url</b> prints the address with its access token.');
    await pause(4500);
    await caption('The overview counts what changed in the chosen range, and shows when the tracker heard each kind of evidence.');
    await pause(5000);
  });
  await step('devices', async () => {
    await highlight(9);
    await nav('Devices');
    await caption('Devices: search by name, address, MAC, registrant or description; sort by presence or last seen.');
    await pause(3000);
    await typeInto(page.getByLabel('Search devices'), 'Siemens');
    await pause(2500);
    await click(page.getByRole('row').filter({ hasText: '00:1b:1b:4a:10:01' }).first());
    await pause(1200);
  });
  await step('describe', async () => {
    await caption('A device’s record: every address and name over time with their sources, its TCP endpoints and its event timeline.');
    await pause(5000);
    await click(page.getByRole('button', { name: 'Describe this device' }));
    await typeInto(page.getByLabel('Description'), 'Press line 1 PLC, cabinet +A12');
    await click(page.getByRole('button', { name: 'Save', exact: true }));
    await caption('Descriptions are the one thing the UI writes: an operator’s note, kept with the host’s history.');
    await pause(4500);
  });

  // 4. Active discovery, planned then run.
  await step('scan', async () => {
    await highlight(10);
    await term(true);
    await caption('Active discovery is planned before it runs: <b>scan plan</b> shows targets, rates and a duration estimate and sends nothing.');
    await cli(['scan', 'plan', '--tcp', '102', '--tcp', '502', '--tcp', '44818'], 5500);
    await caption('<b>scan run</b> then connects within the same safety envelope: one connect at a time per host, closed at once, no payload.');
    await cli(['scan', 'run', '--tcp', '102', '--tcp', '502', '--tcp', '44818'], 5000);
    await term(false);
  });
  await step('services', async () => {
    await nav('TCP endpoints');
    await caption('TCP endpoints: which device answers on which port. A port name is a label, not proof of the protocol.');
    await pause(6500);
  });

  // 5. Changes as they happen.
  await step('late joiner', async () => {
    await nav('Events');
    await caption('Events record each change with its evidence. Now a second drive is connected to line 1…');
    await pause(2500);
    net('late');
    await page.getByText('00:1d:9c:13:00:07').first().waitFor({ timeout: 30000 });
    await caption('…and <b>New device</b> (HOST_DISCOVERED) arrives from its first ARP frame: passive, nothing was sent to it.');
    await pause(5500);
  });
  await step('conflict', async () => {
    await highlight(12);
    await caption('A service laptop is plugged in with a static address copied from the packaging PLC: 172.30.40.11.');
    await pause(2500);
    net('conflict');
    await page.getByText('Duplicate IP', { exact: true }).first().waitFor({ timeout: 45000 });
    await caption('<b>Duplicate IP</b> (DUPLICATE_IP_DETECTED): two MACs claim one address. The Modbus port of .11 now answers from the wrong device.');
    await pause(7000);
  });
  await step('find', async () => {
    await highlight(9);
    await term(true);
    await caption('<b>hosts find</b> answers “who has this address?”, here with both holders and the conflict.');
    await cli(['hosts', 'find', '172.30.40.11'], 7000);
    await term(true);
    await caption('<b>hosts history</b> lists every holder of the address in order, with first and last seen.');
    await cli(['hosts', 'history', '172.30.40.11'], 6500);
    await term(true);
    await caption('<b>events list</b> filters the history by type, address, MAC, interface or time, as table, JSON, JSONL or CSV.');
    await cli(['events', 'list', '--type', 'discovered', '--type', 'duplicate-ip', '--since', '1h'], 7000);
    await term(false);
  });
  await step('dhcp', async () => {
    await nav('DHCP');
    await caption('DHCP: servers against the allowlist, leases and every exchange, passively. The laptop renews its lease every two minutes.');
    await pause(9000);
  });

  // 6. The kill switch.
  await step('kill switch', async () => {
    await highlight(8);
    await term(true);
    await caption('On a sensitive segment, one command stops all active probing, with a reason, and it stays off across restarts.');
    await cli(['active', 'disable', '--reason', 'line 1 commissioning'], 1500);
    await cli(['daemon', 'status'], 5000);
    await cli(['active', 'enable'], 1200);
    await term(false);
  });
  await step('health', async () => {
    await nav('Tracker health');
    await caption('Tracker health: the daemon, its database and every collector and probe engine, with their last passes.');
    await pause(6500);
  });
  await step('end', async () => {
    await nav('Overview');
    await caption('OT Fabric LAN Tracker: one static binary, one SQLite file, a CLI, an API and this UI. MIT licensed.');
    await pause(5000);
    await caption('');
    await pause(600);
  });
} else {
  // Screenshots for the README, after a video run (the conflict is there).
  await step('shots', async () => {
    await nav('Devices');
    await page.waitForTimeout(800);
    await shot('devices');
    await click(page.getByRole('row').filter({ hasText: '00:1b:1b:4a:10:01' }).first());
    await page.waitForTimeout(1200);
    await shot('host-detail');
    await nav('Events');
    await shot('events');
    await nav('TCP endpoints');
    await shot('services');
    await nav('DHCP');
    await shot('dhcp');
    await nav('Tracker health');
    await shot('health');
  });
}

if (video) {
  await cdp.send('Page.stopScreencast');
  await page.waitForTimeout(300);
  const sparse = [];
  for (const f of frames) {
    if (!sparse.length || f.t - sparse.at(-1).t >= 0.04) sparse.push({ ...f });
    else sparse.at(-1).data = f.data;
  }
  const list = [];
  sparse.forEach((f, i) => {
    const name = `frame-${String(i).padStart(5, '0')}.png`;
    writeFileSync(join(out, name), Buffer.from(f.data, 'base64'));
    const next = sparse[i + 1] ? sparse[i + 1].t : f.t + 1;
    list.push(`file '${name}'`, `duration ${(next - f.t).toFixed(4)}`);
  });
  list.push(`file 'frame-${String(sparse.length - 1).padStart(5, '0')}.png'`);
  writeFileSync(join(out, 'frames.txt'), list.join('\n') + '\n');
  const start = frames[0].t;
  console.log('frames', frames.length, 'seconds', (frames.at(-1).t - start + 1).toFixed(1));

  const ffmpeg = (args) => {
    const r = spawnSync('ffmpeg', ['-y', '-loglevel', 'error', ...args], { cwd: out, stdio: 'inherit' });
    if (r.status !== 0) throw new Error(`ffmpeg ${args.join(' ')} failed`);
  };
  // The captions are in the picture only: a soft subtitle track as well
  // would show them twice in players that turn it on by default.
  ffmpeg(['-f', 'concat', '-safe', '0', '-i', 'frames.txt',
    '-vf', 'fps=25,format=yuv420p', '-c:v', 'libx264', '-preset', 'slow', '-crf', '28', '-tune', 'stillimage',
    '-movflags', '+faststart', 'demo.mp4']);
  const parts = sections.map((s) => ({ ss: s.start - start, t: s.seconds }));
  const inputs = parts.flatMap((p) => ['-ss', p.ss.toFixed(2), '-t', String(p.t), '-i', 'demo.mp4']);
  const chain = parts.map((_, i) => `[${i}:v]`).join('') + `concat=n=${parts.length}:v=1:a=0,fps=8,scale=960:-1:flags=lanczos,split[a][b];` +
    '[a]palettegen=max_colors=96:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle';
  ffmpeg([...inputs, '-filter_complex', chain, 'demo.gif']);
  console.log('wrote demo.mp4, demo.gif');
}
await context.close();
await browser.close();
console.log(failures ? `done with ${failures} failed steps` : 'done');
process.exit(failures ? 1 : 0);
