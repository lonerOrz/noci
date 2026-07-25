const cp = require("child_process");
const fs = require("fs");
const http = require("http");

const PROXY_LOG = "/tmp/noci-proxy.log";
const PORT_PATTERN =
  /Proxy running on http:\/\/\[?[a-zA-Z0-9.:-]+\]?:([0-9]+)/;

function start(binPath, port) {
  const fd = fs.openSync(PROXY_LOG, "w");
  const proc = cp.spawn(binPath, ["proxy", "--port", port], {
    detached: true,
    stdio: ["ignore", fd, fd],
  });
  proc.unref();
  return proc;
}

async function waitForReady(proc, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;

  return new Promise((resolve, reject) => {
    const check = setInterval(() => {
      // Process exited before ready
      if (proc.exitCode !== null) {
        clearInterval(check);
        const log = fs.existsSync(PROXY_LOG)
          ? fs.readFileSync(PROXY_LOG, "utf8")
          : "";
        return reject(
          new Error(`Proxy exited with code ${proc.exitCode}: ${log}`),
        );
      }

      // Check log for port
      if (fs.existsSync(PROXY_LOG)) {
        const output = fs.readFileSync(PROXY_LOG, "utf8");
        const match = output.match(PORT_PATTERN);
        if (match) {
          clearInterval(check);
          return resolve(match[1]);
        }
      }

      // Timeout
      if (Date.now() >= deadline) {
        clearInterval(check);
        reject(new Error("Proxy failed to start within timeout"));
      }
    }, 200);
  });
}

async function fetchPublicKey(url) {
  return new Promise((resolve) => {
    http
      .get(`${url}/public-key`, (res) => {
        let data = "";
        res.on("data", (c) => (data += c));
        res.on("end", () =>
          resolve(res.statusCode === 200 ? data.trim() : ""),
        );
      })
      .on("error", () => resolve(""));
  });
}

function kill(proxyPid) {
  try {
    const pid = parseInt(proxyPid, 10);
    if (!isNaN(pid)) process.kill(pid, "SIGTERM");
  } catch {}
}

module.exports = { start, waitForReady, fetchPublicKey, kill, PROXY_LOG };
