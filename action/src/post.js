const cp = require("child_process");
const fs = require("fs");

async function run() {
  const proxyPid = getState("proxy-pid");
  const startTime = parseInt(getState("start-time"), 10);

  if (!proxyPid) return;

  try {
    const signingKey = process.env.NOCI_SIGNING_KEY || "";
    if (!signingKey) return;

    console.log("[noci-action] Scanning built paths...");
    const builtPaths = queryIncrementalPaths(startTime);
    if (builtPaths.length === 0) return;

    const pushProc = cp.spawnSync(
      "/tmp/noci",
      ["push", "--skip-upstream", ...builtPaths],
      {
        stdio: "inherit",
        env: {
          ...process.env,
          HOME: "/tmp",
          NIX_IGNORE_HOME_DIRECTORY_ERROR: "1",
        },
      },
    );

    if (pushProc.status !== 0)
      throw new Error(`push failed: ${pushProc.status}`);
    exportOutput("pushed-count", builtPaths.length.toString());
  } catch (error) {
    fail(error.message);
  } finally {
    try {
      process.kill(parseInt(proxyPid, 10), "SIGTERM");
    } catch (e) {}
  }
}

function queryIncrementalPaths(startTime, skipUpstream) {
  const out = cp.execSync("nix path-info --all --json", {
    encoding: "utf-8",
    maxBuffer: 100 * 1024 * 1024,
    env: { ...process.env, HOME: "/tmp" },
  });
  const pathsData = JSON.parse(out);
  const result = [];
  const filter = (p, info) => {
    if (info.registrationTime >= startTime) {
      if (
        skipUpstream === "true" &&
        info.signatures &&
        info.signatures.length > 0
      )
        return;
      result.push(p);
    }
  };
  Array.isArray(pathsData)
    ? pathsData.forEach((i) => filter(i.path, i))
    : Object.entries(pathsData).forEach(([p, i]) => filter(p, i));
  return result;
}

function getExecCommand(subCommand, extraArgs = []) {
  const localPath = path.join(process.env.GITHUB_WORKSPACE || ".", "noci");
  if (fs.existsSync(localPath))
    return { cmd: localPath, args: [subCommand, ...extraArgs] };
  return {
    cmd: "nix",
    args: [
      "run",
      "--option",
      "sandbox",
      "false",
      "github:lonerOrz/noci#default",
      "--",
      subCommand,
      ...extraArgs,
    ],
  };
}

function getState(k) {
  return process.env[`STATE_noci-state-${k}`];
}
function getEnvOrInput(e, i) {
  return process.env[e] || process.env[`INPUT_${i.toUpperCase()}`];
}
function exportOutput(k, v) {
  fs.appendFileSync(process.env.GITHUB_OUTPUT, `${k}=${v}\n`);
}

run();
