const cp = require("child_process");
const fs = require("fs");
const utils = require("./utils");

async function run() {
  const proxyPid = utils.getState("proxy-pid");

  const registry = utils.getState("registry") || "ghcr.io";
  const repo = utils.getState("repo");
  const token = utils.getState("token");
  const signingKey = utils.getState("signing-key");
  const skipUpstream =
    utils.getEnvOrInput("NOCI_SKIP_UPSTREAM", "skip-upstream") || "true";

  if (!proxyPid || !repo || !signingKey) return;

  try {
    const newPaths = getNewPathsStoreScan(skipUpstream === "true");

    if (newPaths.length === 0) {
      console.log("[noci-action] No new packages built in this job.");
      return;
    }

    console.log(`[noci-action] Found ${newPaths.length} new paths to push.`);

    const pushProc = cp.spawnSync(
      "/tmp/noci",
      [
        "push",
        "--skip-upstream",
        "--repo",
        repo,
        "--registry",
        registry,
        ...newPaths,
      ],
      {
        stdio: "inherit",
        env: {
          ...process.env,
          HOME: "/tmp",
          NIX_IGNORE_HOME_DIRECTORY_ERROR: "1",
          NOCI_REGISTRY: registry,
          NOCI_REPO: repo,
          NOCI_SIGNING_KEY: signingKey,
          NOCI_TOKEN: token,
          GITHUB_TOKEN: token,
        },
      },
    );

    if (pushProc.status !== 0)
      throw new Error(`push failed with exit code: ${pushProc.status}`);

    utils.exportOutput("pushed-count", newPaths.length.toString());
  } catch (error) {
    utils.fail(error.message);
  } finally {
    try {
      process.kill(parseInt(proxyPid, 10), "SIGTERM");
    } catch (e) {}
  }
}

function getNewPathsStoreScan(skipUpstream) {
  if (!fs.existsSync("/tmp/noci-initial-store.txt")) {
    console.log("[noci-action] Initial store snapshot not found. Skipping push.");
    return [];
  }

  const initial = new Set(
    fs.readFileSync("/tmp/noci-initial-store.txt", "utf-8").trim().split("\n"),
  );

  const current = fs.readdirSync("/nix/store");

  const diff = current
    .filter((p) => !initial.has(p) && !p.endsWith(".drv"))
    .map((p) => "/nix/store/" + p);

  if (diff.length === 0) return [];

  const out = cp.execSync(`nix path-info --json ${diff.join(" ")}`, {
    encoding: "utf-8",
    maxBuffer: 50 * 1024 * 1024,
  });

  const infoMap = JSON.parse(out);
  const result = [];

  for (const [p, info] of Object.entries(infoMap)) {
    if (!p || p.endsWith(".drv")) continue;
    if (
      skipUpstream &&
      info.signatures?.some((s) => s.startsWith("cache.nixos.org-1:"))
    )
      continue;
    result.push(p);
  }
  return result;
}

run();
