// Black-box routing contract for the loft-web static file server. It runs the real web/nginx.conf
// (rendered by the nginx image the same way production does) over a temp sites tree, then asserts how
// a miss is served. The load-bearing rule: a document request falls back to the app shell so a
// client-routed SPA gets clean deep links, while a missing asset stays a real 404 and a site that
// ships a 404.html keeps classic per-path 404s. Requests go through curl because the cases are
// distinguished by the Host and Accept headers, and Host is a forbidden header for fetch.
import { before, after, describe, it } from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, chmodSync, readdirSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { docker, sleep, freePort } from "./harness.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, "..");
const NGINX_CONF = join(REPO, "web", "nginx.conf");
const SYS_DIR = join(REPO, "web", "sys");
const NAME = `loft-web-test-${process.pid}`;
const IMAGE = "nginx:1.29-alpine"; // match web/Dockerfile's base

// A real browser navigation Accept, not a bare "text/html": the fallback keys on a substring match
// against this multi-value header, so testing the real value guards against a tightened comparison.
const NAV = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8";
const ASSET = "*/*"; // what a script/stylesheet/fetch sends
const SHELL = "APP-SHELL-MARKER";
const SUBDIR = "SUBDIR-INDEX-MARKER";

let port;
let sitesDir;

function writeSite(root, files) {
  for (const [rel, content] of Object.entries(files)) {
    const f = join(root, rel);
    mkdirSync(dirname(f), { recursive: true });
    writeFileSync(f, content);
  }
}

// The container's nginx worker runs as a non-root uid, and a Linux bind mount preserves host modes,
// so the mounted tree must be traversable/readable by another uid. mkdtemp makes the root 0700, which
// nginx cannot enter; open the whole tree (dirs 0755, files 0644). Without this the sites read as
// empty in a Linux CI runner even though they mount fine on Docker Desktop, which ignores uid.
function openTree(p) {
  const s = statSync(p);
  chmodSync(p, s.isDirectory() ? 0o755 : 0o644);
  if (s.isDirectory()) for (const e of readdirSync(p)) openTree(join(p, e));
}

// docker logs writes the container's stdout (access log) and stderr (nginx's [emerg]/error log) as
// separate streams; fold both in, since a rejected config prints only to stderr and that is exactly
// what we need to see. spawnSync does not throw on a non-zero exit, so a gone container still yields
// its "No such container" message rather than nothing.
const logs = () => {
  const r = spawnSync("docker", ["logs", NAME], { encoding: "utf8" });
  return `${r.stdout || ""}${r.stderr || ""}`.trim() || "(no container logs)";
};

// One curl to <host><path> with the given Accept header. `extra` carries the output-format flags.
// Bounded so a half-open connection can't outlive the readiness deadline and hang the run.
const curl = (host, path, accept, extra = []) =>
  execFileSync(
    "curl",
    ["-s", "--connect-timeout", "2", "--max-time", "5", ...extra, "-H", `Host: ${host}`, "-H", `Accept: ${accept}`, `http://127.0.0.1:${port}${path}`],
    { encoding: "utf8" },
  );

// status + content-type for a request. Splits on the first space only, so a "text/html; charset"
// content type stays intact.
function probe(host, path, accept) {
  const out = curl(host, path, accept, ["-o", "/dev/null", "-w", "%{http_code} %{content_type}"]).trim();
  const sp = out.indexOf(" ");
  return sp === -1 ? { status: out, contentType: "" } : { status: out.slice(0, sp), contentType: out.slice(sp + 1) };
}

const body = (host, path, accept) => curl(host, path, accept);

before(async () => {
  sitesDir = mkdtempSync(join(tmpdir(), "loft-web-sites-"));
  const shell = `<!doctype html><title>app</title><div id="root">${SHELL}</div>`;
  // A SPA: an index.html, an asset, and a real subdirectory with its own index, no 404.html.
  writeSite(join(sitesDir, "spa"), {
    "index.html": shell,
    "assets/app.js": "export const x = 1;",
    "sub/index.html": `<!doctype html>${SUBDIR}`,
  });
  // An opted-out site: same, but with its own 404.html.
  writeSite(join(sitesDir, "classic"), { "index.html": shell, "404.html": `<!doctype html>CUSTOM-404-MARKER`, "assets/app.js": "export const x = 1;" });
  openTree(sitesDir);

  port = await freePort();
  // Self-heal a leftover container from a prior killed run (the name keys on pid, which can recur).
  try { docker(["rm", "-f", NAME], { stdio: "ignore" }); } catch { /* none to remove */ }
  // Not --rm: a crashed nginx must leave its logs behind for the failure paths below.
  // nginx renders web/nginx.conf from /templates, substituting LOFT_DOMAIN just like the real image.
  try {
    docker(
      ["run", "-d", "--name", NAME,
        "-e", "LOFT_DOMAIN=loft.test",
        "-v", `${NGINX_CONF}:/etc/nginx/templates/default.conf.template:ro`,
        "-v", `${SYS_DIR}:/usr/share/nginx/__loft-sys:ro`,
        "-v", `${sitesDir}:/mnt/loft:ro`,
        "-p", `127.0.0.1:${port}:8080`,
        IMAGE],
      { stdio: ["ignore", "ignore", "pipe"] },
    );
  } catch (e) {
    throw new Error(`failed to start loft-web container:\n${e.stderr || e.message}`);
  }

  const deadline = Date.now() + 20_000;
  let ready = false;
  while (Date.now() < deadline) {
    // Fail fast and loud if nginx rejected the config and exited, instead of polling a dead container.
    let running;
    try {
      running = docker(["inspect", "-f", "{{.State.Running}}", NAME], { stdio: ["ignore", "pipe", "pipe"] }).trim();
    } catch (e) {
      throw new Error(`loft-web container vanished during startup:\n${e.stderr || e.message}`);
    }
    if (running !== "true") throw new Error(`loft-web container exited during startup:\n${logs()}`);
    // Swallow connection-refused during warmup, but surface a missing curl instead of timing out on it.
    try { if (probe("spa.loft.test", "/", NAV).status === "200") { ready = true; break; } } catch (e) { if (e.code === "ENOENT") throw e; }
    await sleep(250);
  }
  if (!ready) throw new Error(`loft-web did not become ready on 127.0.0.1:${port} after 20s:\n${logs()}`);
}, { timeout: 120_000 });

after(() => {
  try { docker(["rm", "-f", NAME], { stdio: "ignore" }); } catch { /* already gone */ }
  if (sitesDir) rmSync(sitesDir, { recursive: true, force: true });
});

describe("deployed site routing", () => {
  it("serves a real page and a real asset as themselves", () => {
    assert.equal(probe("spa.loft.test", "/", NAV).status, "200");
    const asset = probe("spa.loft.test", "/assets/app.js", ASSET);
    assert.equal(asset.status, "200");
    assert.match(asset.contentType, /javascript/);
  });

  it("serves a real subdirectory index, not the app shell", () => {
    // A real subdirectory with its own index.html must be served by nginx's normal directory
    // handling, not swallowed by the SPA fallback. If @fallback shadowed it, the body would be the
    // app shell instead of the subdir's index.
    const r = probe("spa.loft.test", "/sub/", NAV);
    assert.equal(r.status, "200");
    assert.match(body("spa.loft.test", "/sub/", NAV), new RegExp(SUBDIR));
  });

  it("redirects a directory request that omits the trailing slash", () => {
    // /sub is a real directory: nginx canonicalizes it to /sub/ with a 301 (the browser then loads
    // /sub/, served above), rather than the SPA fallback answering the no-slash path with a shell.
    const out = curl("spa.loft.test", "/sub", NAV, ["-o", "/dev/null", "-w", "%{http_code} %{redirect_url}"]).trim();
    const sp = out.indexOf(" ");
    assert.equal(out.slice(0, sp), "301");
    assert.match(out.slice(sp + 1), /\/sub\/$/);
  });

  it("falls back to the app shell for a SPA deep link, with a 200", () => {
    const r = probe("spa.loft.test", "/dashboard", NAV);
    assert.equal(r.status, "200");
    assert.match(r.contentType, /text\/html/);
    assert.match(body("spa.loft.test", "/dashboard", NAV), new RegExp(SHELL));
  });

  it("returns a real 404 for a missing asset, never the shell", () => {
    assert.equal(probe("spa.loft.test", "/assets/missing-hash.js", ASSET).status, "404");
  });

  it("does not fall back for a non-document request", () => {
    assert.equal(probe("spa.loft.test", "/dashboard", ASSET).status, "404");
  });

  it("serves a site's own 404.html with a 404 when present", () => {
    const r = probe("classic.loft.test", "/nope", NAV);
    assert.equal(r.status, "404");
    assert.match(body("classic.loft.test", "/nope", NAV), /CUSTOM-404-MARKER/);
  });

  it("keeps missing assets a 404 on an opted-out site", () => {
    assert.equal(probe("classic.loft.test", "/assets/missing-hash.js", ASSET).status, "404");
  });
});
