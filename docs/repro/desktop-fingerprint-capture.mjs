#!/usr/bin/env node
/**
 * 官方客户端请求头抓包复现脚本（本地 mock 网关，不碰真实网关、不消耗额度）。
 *
 * 用途：每次官方 CLI / 桌面客户端发版后，用它逐头核对 internal/fingerprint 与
 * internal/upstream/device.go 的取值是否仍然一致 —— 不必靠读代码猜。
 *
 * 它做三件事：
 *   1. 起一个本地 mock 网关，记录收到的每一个请求头；
 *   2. 加载**官方自己的** ensureSigningProxy（u1s1-cli dist/device-auth.js）与
 *      官方 pi-ai 的 openai-completions 客户端，按官方链路发一次 chat/completions；
 *   3. 再按官方 fetchModels 的路径直连 mock 网关发一次 /v1/models。
 * 输出即「官方客户端真实会发什么头」。
 *
 * 取包：
 *   CLI    ：curl -O https://registry.npmjs.org/u1s1-cli/-/u1s1-cli-<版本>.tgz && tar xzf ...
 *   桌面端：https://u1s1.io/releases/app/latest/dmg（= /releases/app/LATEST 的 0.1.9）
 *          hdiutil attach u1s1_0.1.9_aarch64.dmg -nobrowse -mountpoint /tmp/u1s1-mnt
 *          桌面端把 u1s1-cli 当库用：
 *            <mnt>/u1s1.app/Contents/Resources/resources/server/node_modules/u1s1-cli
 *          自带 Node：<mnt>/u1s1.app/Contents/Resources/resources/Pi Agent Server.app/Contents/MacOS/node
 *
 * 跑法（桌面端链路，SURFACE=desktop）：
 *   U1S1_ROOT=/tmp/u1s1-mnt/u1s1.app/Contents/Resources/resources/server \
 *   SURFACE=desktop \
 *   "$ROOT/../Pi Agent Server.app/Contents/MacOS/node" docs/repro/desktop-fingerprint-capture.mjs
 * 跑法（CLI 链路，SURFACE=terminal；U1S1_ROOT 指向解开的 npm tarball 的 package 目录，
 * 其 node_modules 可从桌面端那份 symlink 过来，openai/pi-ai 版本一致）：
 *   U1S1_ROOT=/tmp/u1s1-re/cli-1.4.1 SURFACE=terminal node docs/repro/desktop-fingerprint-capture.mjs
 *
 * 2026-09-03 用该脚本得到的结论（桌面端 app 0.1.9 = u1s1-cli 1.3.0 + Node 22.23.1）：
 *
 *   POST /v1/chat/completions
 *     user-agent: pi (darwin 25.6.0; arm64)        ← pi-ai getPiUserAgent()，覆盖 SDK 默认值
 *     x-u1s1-client: desktop                        ← ensureSigningProxy(cfg, "desktop", att)
 *     x-u1s1-platform: darwin-arm64
 *     x-u1s1-version: 1.3.0
 *     x-u1s1-attestation: <token>
 *     x-stainless-lang / -package-version / -os / -arch / -runtime / -runtime-version / -retry-count
 *     authorization: DPoP u1s1d-…   dpop: <header.payload.sig>
 *
 *   GET /v1/models
 *     user-agent: undici                            ← Next.js instrumentation 里 undici.install()
 *     x-u1s1-version: 1.3.0
 *     authorization: DPoP u1s1d-…   dpop: …         （无 X-Stainless-*）
 *
 * 关于那个 undici UA：桌面端的 /models /me 是在 Next.js server 里发的，而它的
 * instrumentation.js 启动时先跑 pi-coding-agent 的 configureHttpDispatcher()，
 * 后者调 undici.install() 把 globalThis.fetch 换成独立 undici 8.5.0 的实现。
 * 本脚本用 UNDICI_INSTALL=1 复现这一步（默认开）；去掉它就是 CLI 的
 * user-agent: node（CLI 1.4.1 不装 dispatcher）。
 *
 *   dpop header 段解出来是
 *     {"typ":"dpop+jwt","alg":"ES256","jwk":{"key_ops":["verify"],"ext":true,"kty":"EC","x":…,"y":…,"crv":"P-256"}}
 *   payload 段是
 *     {"jti":"<32hex UUIDv4>","htm":"POST","htu":"…","iat":…,"ath":"…"}
 *
 * 已复核的官方版本（避免重复核对）：
 *
 *   日期        桌面端              CLI            结果
 *   2026-09-03  0.1.9（首次对齐）    1.4.1        发现 desktop/terminal 与 undici/node 两处差异 → v0.9.7 对齐
 *   2026-09-03  0.1.11              1.4.1        **零变化**：内嵌栈与 0.1.9 完全一致
 *                                                （u1s1-cli 1.3.0 / Node 22.23.1 / openai 6.40.0 / undici 8.5.0），
 *                                                device-auth.js 等指纹代码逐字节相同，本脚本实跑输出亦相同；
 *                                                变的只有 sessions/updates/worktrees 等与网关无关的 pi-web 路由
 *
 *   下次真正需要同步的触发条件（任一成立才改代码）：
 *   1. 桌面端内嵌的 u1s1-cli 不再是 1.3.0（看 node_modules/u1s1-cli/package.json）
 *   2. npm u1s1-cli 超过 1.4.1（那是我们 x-u1s1-version 的取值）
 *   3. 本脚本输出出现新增/缺失的头，或 DPoP 结构变化
 */

import { createServer } from 'node:http';
import { webcrypto } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const ROOT = process.env.U1S1_ROOT;
if (!ROOT) {
  console.error('必须设 U1S1_ROOT，指向含 node_modules/u1s1-cli 的目录（桌面端 server/ 或解开的 CLI tarball）');
  process.exit(2);
}
const SURFACE = process.env.SURFACE || 'desktop';
const VERSION = process.env.CLI_VERSION || '1.3.0';
const U = (p) => pathToFileURL(`${ROOT}/node_modules/${p}`).href;

const { ensureSigningProxy } = await import(U('u1s1-cli/dist/device-auth.js'));
const { MODELS } = await import(U('u1s1-cli/dist/config.js'));
const { toProviderModels } = await import(U('u1s1-cli/dist/agent-setup.js'));
const piAi = await import(U('@earendil-works/pi-ai/dist/api/openai-completions.js'));

// 复现桌面端 instrumentation 的 undici.install()：把 globalThis.fetch 换成独立 undici。
// 不装就是 CLI 的 user-agent: node。
if (process.env.UNDICI_INSTALL !== '0') {
  const undici = await import(U('undici/index.js'));
  undici.setGlobalDispatcher(new undici.EnvHttpProxyAgent({ allowH2: false }));
  undici.install?.();
  console.log(`# undici ${undici.VERSION ?? '?'} installed: fetch=${String(globalThis.fetch).slice(0, 40)}…`);
}

// 自造一对 P-256 设备密钥：mock 网关不校验，只需要 DPoP 能自洽签出来。
const pair = await webcrypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify']);
const publicJwk = await webcrypto.subtle.exportKey('jwk', pair.publicKey);
const privateJwk = await webcrypto.subtle.exportKey('jwk', pair.privateKey);

const captured = [];
const gw = createServer(async (req, res) => {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  captured.push({ method: req.method, url: req.url, headers: { ...req.headers } });
  res.writeHead(200, { 'content-type': 'text/event-stream' });
  res.end('data: [DONE]\n\n');
});
await new Promise((r) => gw.listen(0, '127.0.0.1', r));

const cfg = {
  baseUrl: `http://127.0.0.1:${gw.address().port}/v1`,
  apiKey: 'u1s1-fake',
  deviceToken: 'u1s1d-fake-probe',
  devicePublicJwk: publicJwk,
  devicePrivateJwk: privateJwk,
};
const signing = await ensureSigningProxy(cfg, SURFACE, 'ATT_TOKEN');

// 桌面端 models.json 里 u1s1 provider 的形状（api=openai-completions + headers）。
const provider = {
  name: 'u1s1',
  baseUrl: signing.baseUrl,
  api: 'openai-completions',
  apiKey: signing.localKey,
  headers: { 'x-u1s1-version': VERSION },
};
const [m] = toProviderModels(MODELS);
const model = { ...m, baseUrl: provider.baseUrl, api: provider.api, provider: 'u1s1', headers: provider.headers };

const ctx = { messages: [{ role: 'user', content: 'hi', timestamp: Date.now() }], systemPrompt: undefined, tools: [] };
for await (const _ of piAi.stream(model, ctx, { apiKey: provider.apiKey })) { /* drain */ }

// 辅助端点：官方 fetchModels 用裸 fetch 直连网关（不经签名代理）。
const { fetchModels } = await import(U('u1s1-cli/dist/api.js'));
await fetchModels(cfg).catch(() => { /* mock 返回的不是模型列表，忽略 */ });

console.log(`### SURFACE=${SURFACE} CLI_VERSION=${VERSION} captured=${captured.length}`);
for (const c of captured) {
  console.log(`\n${c.method} ${c.url}`);
  for (const [k, v] of Object.entries(c.headers)) {
    const shown = k === 'dpop' ? `${v.slice(0, 24)}…（见下方解码）` : v;
    console.log(`  ${k}: ${shown}`);
  }
  if (c.headers.dpop) {
    const [h, p] = c.headers.dpop.split('.');
    const dec = (s) => Buffer.from(s, 'base64url').toString('utf8');
    console.log(`  → dpop.header : ${dec(h)}`);
    console.log(`  → dpop.payload: ${dec(p)}`);
  }
}
gw.close();
process.exit(0);
