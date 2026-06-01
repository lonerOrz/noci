const cp = require("child_process");
const fs = require("fs");
const path = require("path");
const os = require("os");
const http = require("http");
const https = require("https");

async function run() {
  try {
    const startTime = Math.floor(Date.now() / 1000) - 2;
    saveState("start-time", startTime.toString());

    const binPath = await prepareBinary();

    const registry = getEnvOrInput("NOCI_REGISTRY", "registry") || "ghcr.io";
    let repo = getEnvOrInput("NOCI_REPO", "repo");
    if (!repo && process.env.GITHUB_REPOSITORY)
      repo = process.env.GITHUB_REPOSITORY;
    const token =
      getEnvOrInput("NOCI_TOKEN", "token") || process.env.GITHUB_TOKEN || "";
    const signingKey =
      getEnvOrInput("NOCI_SIGNING_KEY", "signing-key") ||
      process.env.NOCI_SIGNING_KEY ||
      "";
    const proxyPort = getEnvOrInput("NOCI_PROXY_PORT", "proxy-port") || "0";

    if (!repo) throw new Error("Repository is required.");

    process.env.NOCI_REGISTRY = registry;
    process.env.NOCI_REPO = repo;
    process.env.NOCI_TOKEN = token;
    if (signingKey) process.env.NOCI_SIGNING_KEY = signingKey;

    console.log(
      `[noci-action] Launching proxy: ${binPath} proxy --port ${proxyPort}`,
    );

    const proxyProcess = cp.spawn(binPath, ["proxy", "--port", proxyPort], {
      stdio: ["ignore", "pipe", "pipe"],
      env: {
        ...process.env,
        HOME: "/tmp",
        NIX_IGNORE_HOME_DIRECTORY_ERROR: "1",
      },
    });

    saveState("proxy-pid", proxyProcess.pid.toString());

    const port = await waitForProxyPort(proxyProcess);
    const proxyUrl = `http://127.0.0.1:${port}`;
    console.log(`[noci-action] Proxy running on ${proxyUrl}`);
    exportOutput("proxy-url", proxyUrl);

    const pubKey = await fetchPublicKey(proxyUrl);
    if (pubKey) {
      exportVariable(
        "NIX_CONFIG",
        `extra-substituters = ${proxyUrl}\nextra-trusted-public-keys = ${pubKey}\nfallback = true`,
      );
    }
  } catch (error) {
    fail(error.message);
  }
}

async function prepareBinary() {
  const target = "/tmp/noci";
  if (fs.existsSync(target)) return target;

  const version = process.env.GITHUB_ACTION_REF || "v1.0.0";
  const repo = process.env.GITHUB_ACTION_REPOSITORY || "lonerOrz/noci";
  const platform = os.platform();
  const arch = os.arch() === "x64" ? "amd64" : "arm64";
  const releaseUrl = `https://github.com/${repo}/releases/download/${version}/noci-${platform}-${arch}`;

  console.log(
    `[noci-action] Trying to download pre-built binary from ${releaseUrl}...`,
  );
  try {
    await downloadFile(releaseUrl, target);
    console.log("[noci-action] Successfully downloaded pre-built binary.");
    return target;
  } catch (err) {
    console.log(`[noci-action] Release download failed (${err.message}).`);
    console.log(
      "[noci-action] Falling back to building from local source via Nix...",
    );
  }

  try {
    const actionRoot = path.resolve(__dirname, "../.."); // 定位到当前 Action 的源码根目录
    const outLink = "/tmp/noci-result";

    if (fs.existsSync(outLink))
      fs.rmSync(outLink, { recursive: true, force: true });
    if (fs.existsSync(target)) fs.unlinkSync(target);

    console.log(
      `[noci-action] Building local source at ${actionRoot} via Nix build...`,
    );
    cp.execSync(
      `nix build "${actionRoot}" --out-link ${outLink} --option sandbox false`,
      {
        stdio: "inherit",
        env: {
          ...process.env,
          HOME: "/tmp",
          NIX_IGNORE_HOME_DIRECTORY_ERROR: "1",
        },
      },
    );

    const actualBinary = path.join(outLink, "bin/noci");
    if (fs.existsSync(actualBinary)) {
      fs.symlinkSync(actualBinary, target);
      console.log(
        `[noci-action] Local build successful. Created symlink at ${target}`,
      );
      return target;
    }
    throw new Error(
      "Nix build succeeded but 'bin/noci' was not found in output.",
    );
  } catch (err) {
    throw new Error(
      `Failed to prepare noci binary via all strategies: ${err.message}`,
    );
  }
}

function downloadFile(url, targetPath) {
  return new Promise((resolve, reject) => {
    https
      .get(url, (res) => {
        if (res.statusCode === 301 || res.statusCode === 302) {
          return downloadFile(res.headers.location, targetPath)
            .then(resolve)
            .catch(reject);
        }
        if (res.statusCode !== 200) {
          return reject(new Error(`HTTP ${res.statusCode}`));
        }
        const file = fs.createWriteStream(targetPath);
        res.pipe(file);
        file.on("finish", () => {
          fs.chmodSync(targetPath, "755");
          resolve();
        });
      })
      .on("error", reject);
  });
}

function waitForProxyPort(proc) {
  return new Promise((resolve, reject) => {
    let output = "";
    proc.stdout.on("data", (data) => {
      output += data.toString();
      process.stderr.write(data);
      const match = output.match(
        /Proxy running on http:\/\/\[?[a-zA-Z0-9\.-:]+\]?:([0-9]+)/,
      );
      if (match) resolve(match[1]);
    });
    proc.stderr.on("data", (data) => process.stderr.write(data));
    proc.on("close", (code) => {
      if (code !== 0) reject(new Error(`Proxy failed: ${code}`));
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

function saveState(k, v) {
  fs.appendFileSync(process.env.GITHUB_STATE, `noci-state-${k}=${v}\n`);
}
function exportVariable(k, v) {
  fs.appendFileSync(process.env.GITHUB_ENV, `${k}=${v}\n`);
}
function exportOutput(k, v) {
  fs.appendFileSync(process.env.GITHUB_OUTPUT, `${k}=${v}\n`);
}
function getEnvOrInput(e, i) {
  return process.env[e] || process.env[`INPUT_${i.toUpperCase()}`];
}
function fail(msg) {
  console.error(`[noci-action-error] ${msg}`);
  process.exit(1);
}

run();
