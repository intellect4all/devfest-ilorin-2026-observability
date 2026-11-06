// node qrcheck.mjs <image.png>: decode the QR code in an image (a PNG from
// make subscribe PNG=1, or a screenshot of the terminal) and print its text.
// Decodes inside Chromium with jsQR, so no native image libraries are needed.
import { chromium } from "playwright";
import { readFileSync } from "node:fs";

const [file] = process.argv.slice(2);
const jsqr = readFileSync(new URL("./node_modules/jsqr/dist/jsQR.js", import.meta.url), "utf8");
const png = readFileSync(file).toString("base64");
const browser = await chromium.launch();
const page = await browser.newPage();
await page.addScriptTag({ content: jsqr });
const text = await page.evaluate(async (b64) => {
  const img = new Image();
  img.src = `data:image/png;base64,${b64}`;
  await img.decode();
  const c = document.createElement("canvas");
  c.width = img.width;
  c.height = img.height;
  const ctx = c.getContext("2d");
  ctx.drawImage(img, 0, 0);
  const d = ctx.getImageData(0, 0, c.width, c.height);
  const r = window.jsQR(d.data, d.width, d.height, { inversionAttempts: "attemptBoth" });
  return r ? r.data : null;
}, png);
await browser.close();
console.log(JSON.stringify({ file, text }));
process.exit(text ? 0 : 2);
