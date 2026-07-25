const cp = require("child_process");
const fs = require("fs");
const { kill } = require("./proxy");
const utils = require("./utils");

async function run() {
  const proxyPid = utils.getState("proxy-pid");
  const registry = utils.getState("registry") || "ghcr.io";
  const repo = utils.getState("repo");
  const signingKey =
    process.env.NOCI_SIGNING_KEY || process.env.INPUT_SIGNING_KEY;

  if (!signingKey) {
    console.log(
      "[noci-action] No signing key. Operating in Fetch-Only mode (skipping push).",
    );
    cleanup(proxyPid);
    return;
  }

  try {
    const paths = collectPaths();
    if (!paths) {
      cleanup(proxyPid);
      return;
    }

    console.log(`[noci-action] Pushing ${paths.length} built paths...`);

    const token =
      process.env.NOCI_TOKEN ||
      process.env.INPUT_TOKEN ||
      process.env.GITHUB_TOKEN;

    const pushArgs = buildPushArgs();
    await push(registry, repo, signingKey, token, pushArgs, paths);

    utils.exportOutput("pushed-count", paths.length.toString());
    console.log(
      `[noci-action] Successfully pushed ${paths.length} packages.`,
    );
  } catch (error) {
    const failOnError = utils.getState("fail-on-error") === "true";
    if (failOnError) {
      utils.fail(error.message);
    } else {
      console.warn(`[noci-action] Push warning: ${error.message}`);
    }
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

function buildPushArgs() {
  const args = ["push"];

  const compression = utils.getState("compression") || "zstd";
  args.push("--compression", compression);

  const level = utils.getState("compression-level") || "3";
  args.push("--compression-level", level);

  const jobs = utils.getState("jobs") || "0";
  args.push("--jobs", jobs);

  const skipUpstream = utils.getState("skip-upstream") || "true";
  if (skipUpstream === "false") {
    args.push("--skip-upstream=false");
  }

  return args;
}

function push(registry, repo, signingKey, token, pushArgs, paths) {
  return new Promise((resolve, reject) => {
    const proc = cp.spawn("/tmp/noci", pushArgs, {
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
      else reject(new Error(`noci push exited with code ${code}`));
    });
  });
}

function cleanup(proxyPid) {
  for (const key of ["hook-log-path", "hook-script-path"]) {
    const f = utils.getState(key);
    if (f) try { fs.unlinkSync(f); } catch {}
  }
  kill(proxyPid);
}

run();
