#!/usr/bin/env node
// Refresh the public card after its real-app synthetic screenshot:
// node scripts/app-browser-smoke.mjs --round4-synthetic --edge-screenshot docs/social/canary-app-edge.png
// node scripts/render-social-preview.mjs
// The screenshot and card use synthetic data; no network or live app is read.
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { launchBrowser, loadPlaywright } from "./lib-app-browser.mjs";

const screenshot = await readFile(new URL("../docs/social/canary-app-edge.png", import.meta.url));
const output = fileURLToPath(new URL("../docs/social/social-preview.png", import.meta.url));
const playwright = loadPlaywright("render-social-preview");
const { browser } = await launchBrowser(playwright.chromium, "chromium", { headless: true });
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 640 }, deviceScaleFactor: 1 });
  const externalRequests = [];
  page.on("request", (request) => {
    if (!request.url().startsWith("data:")) externalRequests.push(request.url());
  });
  await page.route("**/*", (route) => route.abort());
  await page.setContent(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Canary social preview</title>
<style>
* { box-sizing: border-box; }
body { margin: 0; width: 1280px; height: 640px; background: #f8f6f1; color: #111827; font-family: Arial, sans-serif; border-top: 12px solid #087e70; }
.brand { position: absolute; top: 77px; left: 64px; color: #008774; font-size: 32px; font-weight: 700; }
h1 { position: absolute; left: 64px; top: 135px; margin: 0; font-size: 54px; line-height: 1.19; letter-spacing: -1px; }
.description { position: absolute; left: 64px; top: 388px; margin: 0; font-size: 25px; line-height: 1.48; color: #455875; }
.boundary { position: absolute; left: 64px; top: 526px; margin: 0; color: #008774; font-size: 21px; font-weight: 700; }
figure { position: absolute; left: 689px; top: 95px; width: 551px; margin: 0; }
img { display: block; width: 551px; height: 459px; border: 1px solid #455875; border-radius: 13px; box-shadow: 0 18px 32px #18243535; }
figcaption { margin-top: 18px; color: #536582; font-size: 18px; white-space: nowrap; }
</style></head><body>
<div class="brand">Canary</div>
<h1>Know what changed.<br>Know what needs<br>attention.</h1>
<p class="description">Daily brief, live book, and broker-confirmed history<br>from your local TWS or IB Gateway session.</p>
<p class="boundary">Standard binary + every MCP tool: read-only</p>
<figure><img src="data:image/png;base64,${screenshot.toString("base64")}" alt="Canary Edge with a synthetic decision review and price outcomes"><figcaption>Canary Edge · synthetic data · observed history, not a forecast</figcaption></figure>
</body></html>`);
  await page.locator("img").evaluate((image) => image.decode());
  if (externalRequests.length) throw new Error(`Social card attempted external requests: ${externalRequests.length}`);
  await page.screenshot({ path: output, animations: "disabled" });
  console.log(JSON.stringify({ output, width: 1280, height: 640, synthetic_only: true, external_requests: externalRequests.length }));
} finally {
  await browser.close();
}
