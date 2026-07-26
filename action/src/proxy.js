const cp = require("child_process");
const fs = require("fs");
const http = require("http");
const utils = require("./utils");

const PORT_PATTERN = /Proxy running on http:\/\/\[?[a-zA-Z0-9.:-]+\]?:([0-9]+)/;

async function startProxy(binPath, proxyPort) {
  const runId = process.env.GITHUB_RUN_ID || "default";
  const runAttempt = process.env.GITHUB_RUN_ATTEMPT || "1";
  const suffix = `${runId}-${runAttempt}`;

  // Isolated paths per run attempt — prevents cross-job collisions on self-hosted runners
  const hookLogPath = `/tmp/noci-build-paths-${suffix}.log`;
  const hookScriptPath = `/tmp/noci-hook-${suffix}.sh`;
  utils.saveState("hook-log-path", hookLogPath);
  utils.saveState("hook-script-path", hookScriptPath);

  // Write post-build-hook that collects built paths
  fs.writeFileSync(
    hookScriptPath,
    `#!/bin/sh
for path in $OUT_PATHS; do
  if [ -n "$path" ]; then
    echo "$path" >> ${hookLogPath}
  fi
done`,
    { mode: 0o755 },
  );

  // Start proxy daemon
  const logPath = `/tmp/noci-proxy-${suffix}.log`;
  const logFd = fs.openSync(logPath, "w");
  const proc = cp.spawn(binPath, ["proxy", "--port", proxyPort], {
    detached: true,
    stdio: ["ignore", logFd, logFd],
  });
  proc.unref();
  utils.saveState("proxy-pid", proc.pid.toString());

  // Wait for port + fetch public key + configure Nix
  const port = await waitForPort(proc, logPath);
  const proxyUrl = `http://127.0.0.1:${port}`;
  utils.exportOutput("proxy-url", proxyUrl);

  const pubKey = await fetchPublicKey(proxyUrl);
  configureNix(hookScriptPath, proxyUrl, pubKey);

  console.log(`[noci-action] Proxy active at ${proxyUrl}`);
}

function waitForPort(proc, logPath, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;

  return new Promise((resolve, reject) => {
    const timer = setInterval(() => {
      if (proc.exitCode !== null) {
        clearInterval(timer);
        const log = fs.existsSync(logPath)
          ? fs.readFileSync(logPath, "utf8")
          : "";
        return reject(
          new Error(`Proxy exited with code ${proc.exitCode}: ${log}`),
        );
      }

      if (fs.existsSync(logPath)) {
        const content = fs.readFileSync(logPath, "utf8");
        const match = content.match(PORT_PATTERN);
        if (match) {
          clearInterval(timer);
          return resolve(match[1]);
        }
      }

      if (Date.now() >= deadline) {
        clearInterval(timer);
        reject(new Error("Proxy failed to start within timeout"));
      }
    }, 150);
  });
}

function fetchPublicKey(url) {
  return new Promise((resolve) => {
    http
      .get(`${url}/public-key`, (res) => {
        let data = "";
        res.on("data", (c) => (data += c));
        res.on("end", () => resolve(res.statusCode === 200 ? data.trim() : ""));
      })
      .on("error", () => resolve(""));
  });
}

function configureNix(hookScriptPath, proxyUrl, pubKey) {
  const parts = [`post-build-hook = ${hookScriptPath}`];
  if (pubKey) {
    parts.push(`extra-substituters = ${proxyUrl}`);
    parts.push(`extra-trusted-public-keys = ${pubKey}`);
    parts.push("fallback = true");
  }
  utils.exportVariable("NIX_CONFIG", parts.join("\n"));
}

function kill(proxyPid) {
  if (!proxyPid) return;
  try {
    const pid = parseInt(proxyPid, 10);
    if (!isNaN(pid)) process.kill(pid, "SIGTERM");
  } catch {}
}

module.exports = { startProxy, kill };
