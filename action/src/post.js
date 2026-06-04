const cp = require("child_process");
const fs = require("fs");
const utils = require("./utils");

async function run() {
  const proxyPid = utils.getState("proxy-pid");

  const registry = utils.getState("registry") || "ghcr.io";
  const repo = utils.getState("repo");
  const token = utils.getState("token");
  const signingKey = utils.getState("signing-key");

  if (!proxyPid || !repo || !signingKey) return;

  try {
    const hookLogPath = "/tmp/noci-build-paths.log";
    if (!fs.existsSync(hookLogPath)) {
      console.log("[noci-action] No build paths recorded. Skipping push.");
      return;
    }

    const rawPaths = fs.readFileSync(hookLogPath, "utf-8").trim();
    if (!rawPaths) return;

    const paths = Array.from(
      new Set(rawPaths.split("\n").filter((p) => p && !p.endsWith(".drv"))),
    );

    if (paths.length === 0) {
      console.log("[noci-action] No new paths to push.");
      return;
    }

    console.log(
      `[noci-action] Pushing ${paths.length} built paths via stdin...`,
    );

    await new Promise((resolve, reject) => {
      const pushProc = cp.spawn("/tmp/noci", ["push"], {
        stdio: ["pipe", "inherit", "inherit"],
        env: utils.getSafeEnv({
          NOCI_REGISTRY: registry,
          NOCI_REPO: repo,
          NOCI_SIGNING_KEY: signingKey,
          NOCI_TOKEN: token,
          GITHUB_TOKEN: token,
        }),
      });

      pushProc.stdin.write(paths.join("\n"));
      pushProc.stdin.end();

      pushProc.on("close", (code) => {
        if (code === 0) resolve();
        else reject(new Error(`push failed with exit code: ${code}`));
      });
    });

    utils.exportOutput("pushed-count", paths.length.toString());
  } catch (error) {
    utils.fail(error.message);
  } finally {
    try {
      fs.unlinkSync("/tmp/noci-build-paths.log");
    } catch (e) {}
    try {
      fs.unlinkSync("/tmp/noci-hook.sh");
    } catch (e) {}
    try {
      const pid = parseInt(proxyPid, 10);
      if (!isNaN(pid)) process.kill(pid, "SIGTERM");
    } catch (e) {}
  }
}

run();
