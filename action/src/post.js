const cp = require("child_process");
const fs = require("fs");
const { kill } = require("./proxy");
const utils = require("./utils");

async function run() {
  const proxyPid = utils.getState("proxy-pid");
  const repo = utils.getState("repo");
  const signingKey =
    process.env.NOCI_SIGNING_KEY || process.env.INPUT_SIGNING_KEY;

  // Nothing to push if no proxy, no repo, or no signing key
  if (!proxyPid || !repo || !signingKey) return;

  try {
    const paths = collectPaths();
    if (!paths) return;

    console.log(`[noci-action] Pushing ${paths.length} built paths...`);

    const registry = utils.getState("registry") || "ghcr.io";
    const token =
      process.env.NOCI_TOKEN || process.env.INPUT_TOKEN || process.env.GITHUB_TOKEN;

    await push(registry, repo, signingKey, token, paths);

    utils.exportOutput("pushed-count", paths.length.toString());
  } catch (error) {
    utils.fail(error.message);
  } finally {
    cleanup(proxyPid);
  }
}

function collectPaths() {
  const hookLogPath =
    utils.getState("hook-log-path") || "/tmp/noci-build-paths.log";

  if (!fs.existsSync(hookLogPath)) {
    console.log("[noci-action] No build paths recorded. Skipping push.");
    return null;
  }

  const raw = fs.readFileSync(hookLogPath, "utf-8").trim();
  if (!raw) return null;

  const paths = [
    ...new Set(raw.split("\n").filter((p) => p && !p.endsWith(".drv"))),
  ];

  if (paths.length === 0) {
    console.log("[noci-action] No new paths to push.");
    return null;
  }

  return paths;
}

function push(registry, repo, signingKey, token, paths) {
  return new Promise((resolve, reject) => {
    const proc = cp.spawn("/tmp/noci", ["push"], {
      stdio: ["pipe", "inherit", "inherit"],
      env: utils.getSafeEnv({
        NOCI_REGISTRY: registry,
        NOCI_REPO: repo,
        NOCI_SIGNING_KEY: signingKey,
        NOCI_TOKEN: token,
        GITHUB_TOKEN: token,
      }),
    });

    proc.stdin.write(paths.join("\n"));
    proc.stdin.end();
    proc.on("close", (code) => {
      if (code === 0) resolve();
      else reject(new Error(`push failed with exit code: ${code}`));
    });
  });
}

function cleanup(proxyPid) {
  // Remove temp files
  for (const key of ["hook-log-path", "hook-script-path"]) {
    const f = utils.getState(key);
    if (f) try { fs.unlinkSync(f); } catch {}
  }
  // Stop proxy
  kill(proxyPid);
}

run();
