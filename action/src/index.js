const cp = require("child_process");
const fs = require("fs");
const path = require("path");
const os = require("os");
const http = require("http");
const utils = require("./utils");

async function run() {
  try {
    const binPath = await prepareBinary();

    const registry =
      utils.getEnvOrInput("NOCI_REGISTRY", "registry") || "ghcr.io";
    const repo =
      utils.getEnvOrInput("NOCI_REPO", "repo") || process.env.GITHUB_REPOSITORY;
    const token =
      utils.getEnvOrInput("NOCI_TOKEN", "token") ||
      process.env.GITHUB_TOKEN ||
      "";
    const signingKey =
      utils.getEnvOrInput("NOCI_SIGNING_KEY", "signing-key") ||
      process.env.NOCI_SIGNING_KEY ||
      "";
    const proxyPort =
      utils.getEnvOrInput("NOCI_PROXY_PORT", "proxy-port") || "0";

    if (!repo) throw new Error("Repository is required.");

    utils.saveState("registry", registry);
    utils.saveState("repo", repo);
    utils.saveState("token", token);
    utils.saveState("signing-key", signingKey);

    process.env.NOCI_REGISTRY = registry;
    process.env.NOCI_REPO = repo;
    process.env.NOCI_TOKEN = token;
    if (signingKey) process.env.NOCI_SIGNING_KEY = signingKey;

    // Unique log path per run attempt — avoids cross-job collisions on self-hosted runners
    const runId = process.env.GITHUB_RUN_ID || "default";
    const runAttempt = process.env.GITHUB_RUN_ATTEMPT || "1";
    const hookLogPath = `/tmp/noci-build-paths-${runId}-${runAttempt}.log`;
    utils.saveState("hook-log-path", hookLogPath);

    fs.writeFileSync(
      "/tmp/noci-hook.sh",
      `#!/bin/sh
for path in $OUT_PATHS; do
  if [ -n "$path" ]; then
    echo "$path" >> ${hookLogPath}
  fi
done`,
      { mode: 0o755 },
    );

    const logPath = "/tmp/noci-proxy.log";
    const logFd = fs.openSync(logPath, "w");

    const proxyProcess = cp.spawn(binPath, ["proxy", "--port", proxyPort], {
      detached: true,
      stdio: ["ignore", logFd, logFd],
    });

    proxyProcess.unref();
    utils.saveState("proxy-pid", proxyProcess.pid.toString());

    const port = await waitForProxyPort(proxyProcess, logPath);
    const proxyUrl = `http://127.0.0.1:${port}`;
    console.log(`[noci-action] Proxy running on ${proxyUrl}`);
    utils.exportOutput("proxy-url", proxyUrl);

    const pubKey = await fetchPublicKey(proxyUrl);

    const nixConfigParts = [];
    nixConfigParts.push("post-build-hook = /tmp/noci-hook.sh");
    if (pubKey) {
      nixConfigParts.push(`extra-substituters = ${proxyUrl}`);
      nixConfigParts.push(`extra-trusted-public-keys = ${pubKey}`);
      nixConfigParts.push("fallback = true");
    }
    utils.exportVariable("NIX_CONFIG", nixConfigParts.join("\n"));
  } catch (error) {
    utils.fail(error.message);
  }
}

async function prepareBinary() {
  const target = "/tmp/noci";
  if (fs.existsSync(target)) return target;

  const actionRoot = path.resolve(__dirname, "../..");

  const candidates = [
    path.join(actionRoot, "action/noci"),
    path.join(actionRoot, "noci"),
    path.join(__dirname, "../noci"),
  ];
  for (const p of candidates) {
    if (fs.existsSync(p)) {
      console.log(`[noci-action] Using pre-built binary at ${p}`);
      try {
        fs.symlinkSync(p, target);
      } catch (e) {
        fs.copyFileSync(p, target);
        fs.chmodSync(target, "755");
      }
      return target;
    }
  }

  const version = process.env.GITHUB_ACTION_REF;
  const repo = process.env.GITHUB_ACTION_REPOSITORY || "lonerOrz/noci";
  const platform = os.platform();
  const arch = os.arch() === "x64" ? "amd64" : "arm64";
  const binaryName = `noci-${platform}-${arch}`;

  if (version) {
    const tag = version.startsWith("v") ? version : `v${version}`;
    const releaseUrl = `https://github.com/${repo}/releases/download/${tag}/${binaryName}`;
    try {
      await downloadFile(releaseUrl, target);
      return target;
    } catch (err) {
      console.log(`[noci-action] Download ${tag} failed, trying latest release...`);
    }
  }

  const latestUrl = `https://github.com/${repo}/releases/latest/download/${binaryName}`;
  try {
    await downloadFile(latestUrl, target);
    return target;
  } catch (err) {
    console.log(`[noci-action] Latest release download failed, building locally...`);
  }

  const outLink = "/tmp/noci-result";
  if (fs.existsSync(outLink))
    fs.rmSync(outLink, { recursive: true, force: true });
  if (fs.existsSync(target)) fs.unlinkSync(target);

  cp.execSync(`nix build "${actionRoot}" --out-link ${outLink}`, {
    stdio: "inherit",
    env: utils.getSafeEnv(),
  });

  const actualBinary = path.join(outLink, "bin/noci");
  if (!fs.existsSync(actualBinary))
    throw new Error("Build succeeded but binary not found.");
  fs.symlinkSync(actualBinary, target);
  return target;
}

async function downloadFile(url, targetPath) {
  const res = await fetch(url, { redirect: "follow" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const file = fs.createWriteStream(targetPath);
  for await (const chunk of res.body) file.write(chunk);
  file.end();
  await new Promise((r) => file.on("finish", r));
  fs.chmodSync(targetPath, "755");
}

function waitForProxyPort(proc, logPath) {
  return new Promise((resolve, reject) => {
    const interval = setInterval(() => {
      if (!fs.existsSync(logPath)) return;
      const output = fs.readFileSync(logPath, "utf8");
      const match = output.match(
        /Proxy running on http:\/\/\[?[a-zA-Z0-9.:-]+\]?:([0-9]+)/,
      );
      if (match) {
        clearInterval(interval);
        resolve(match[1]);
      }
    }, 200);

    proc.on("close", (code) => {
      clearInterval(interval);
      if (code !== 0) {
        if (fs.existsSync(logPath))
          console.error(fs.readFileSync(logPath, "utf8"));
        reject(new Error(`Proxy failed with exit code: ${code}`));
      }
    });
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

run();
