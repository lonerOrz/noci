const cp = require("child_process");
const fs = require("fs");
const path = require("path");
const utils = require("./utils");

async function run() {
  const proxyPid = utils.getState("proxy-pid");
  const startTime = parseInt(utils.getState("start-time"), 10);

  const registry = utils.getState("registry") || "ghcr.io";
  const repo = utils.getState("repo");
  const token = utils.getState("token");
  const signingKey = utils.getState("signing-key");
  const skipUpstream =
    utils.getEnvOrInput("NOCI_SKIP_UPSTREAM", "skip-upstream") || "true";

  if (!proxyPid || !repo || !signingKey) return;

  try {
    const builtPaths = getBuildOutputs();
    const closurePaths = getClosure(builtPaths);
    const filteredPaths = filterPaths(
      closurePaths,
      startTime,
      skipUpstream === "true",
    );

    if (filteredPaths.length === 0) return;

    const pushProc = cp.spawnSync(
      "/tmp/noci",
      [
        "push",
        "--skip-upstream",
        "--repo",
        repo,
        "--registry",
        registry,
        ...filteredPaths,
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

    utils.exportOutput("pushed-count", filteredPaths.length.toString());
  } catch (error) {
    utils.fail(error.message);
  } finally {
    try {
      process.kill(parseInt(proxyPid, 10), "SIGTERM");
    } catch (e) {}
  }
}

function getBuildOutputs() {
  const buildResult = path.join(process.env.GITHUB_WORKSPACE || ".", "result");
  if (!fs.existsSync(buildResult)) return [];
  return [fs.readlinkSync(buildResult)];
}

function getClosure(paths) {
  if (paths.length === 0) return [];
  return cp
    .execSync(`nix-store -qR ${paths.join(" ")}`, { encoding: "utf-8" })
    .trim()
    .split("\n");
}

function filterPaths(paths, startTime, skipUpstream) {
  if (paths.length === 0) return [];
  const out = cp.execSync(`nix path-info --json --sigs ${paths.join(" ")}`, {
    encoding: "utf-8",
    maxBuffer: 50 * 1024 * 1024,
  });

  const infoMap = JSON.parse(out);
  const result = [];
  const entries = Array.isArray(infoMap)
    ? infoMap
    : Object.keys(infoMap).map((k) => ({ path: k, ...infoMap[k] }));

  entries.forEach((info) => {
    const p = info.path;
    if (!p || typeof p !== "string" || p.endsWith(".drv")) return;
    if (info.registrationTime >= startTime) {
      if (
        skipUpstream &&
        info.signatures?.some((s) => s.startsWith("cache.nixos.org-1:"))
      )
        return;
      result.push(p);
    }
  });

  return result;
}

run();
