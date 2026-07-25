const fs = require("fs");
const path = require("path");
const { ensureBinary } = require("./binary");
const proxy = require("./proxy");
const utils = require("./utils");

async function run() {
  try {
    const actionRoot = path.resolve(__dirname, "../..");
    const binPath = await ensureBinary(actionRoot);
    const config = resolveConfig();

    // Start proxy
    const proc = proxy.start(binPath, config.proxyPort);
    utils.saveState("proxy-pid", proc.pid.toString());

    const port = await proxy.waitForReady(proc);
    const proxyUrl = `http://127.0.0.1:${port}`;
    console.log(`[noci-action] Proxy running on ${proxyUrl}`);
    utils.exportOutput("proxy-url", proxyUrl);

    // Configure Nix substituter
    const pubKey = await proxy.fetchPublicKey(proxyUrl);
    configureNix(config.hookScriptPath, proxyUrl, pubKey);
  } catch (error) {
    utils.fail(error.message);
  }
}

function resolveConfig() {
  const runId = process.env.GITHUB_RUN_ID || "default";
  const attempt = process.env.GITHUB_RUN_ATTEMPT || "1";
  const suffix = `${runId}-${attempt}`;

  const config = {
    registry:
      utils.getEnvOrInput("NOCI_REGISTRY", "registry") || "ghcr.io",
    repo:
      utils.getEnvOrInput("NOCI_REPO", "repo") ||
      process.env.GITHUB_REPOSITORY,
    token:
      utils.getEnvOrInput("NOCI_TOKEN", "token") ||
      process.env.GITHUB_TOKEN ||
      "",
    signingKey:
      utils.getEnvOrInput("NOCI_SIGNING_KEY", "signing-key") ||
      process.env.NOCI_SIGNING_KEY ||
      "",
    proxyPort:
      utils.getEnvOrInput("NOCI_PROXY_PORT", "proxy-port") || "0",
    hookLogPath: `/tmp/noci-build-paths-${suffix}.log`,
    hookScriptPath: `/tmp/noci-hook-${suffix}.sh`,
  };

  if (!config.repo) throw new Error("Repository is required.");

  // Persist for post step
  utils.saveState("registry", config.registry);
  utils.saveState("repo", config.repo);
  utils.saveState("hook-log-path", config.hookLogPath);
  utils.saveState("hook-script-path", config.hookScriptPath);

  // Export to subsequent steps
  utils.exportVariable("NOCI_REGISTRY", config.registry);
  utils.exportVariable("NOCI_REPO", config.repo);
  utils.exportVariable("NOCI_TOKEN", config.token);
  if (config.signingKey) utils.exportVariable("NOCI_SIGNING_KEY", config.signingKey);

  // Write post-build hook that collects built paths
  fs.writeFileSync(
    config.hookScriptPath,
    `#!/bin/sh
for path in $OUT_PATHS; do
  if [ -n "$path" ]; then
    echo "$path" >> ${config.hookLogPath}
  fi
done`,
    { mode: 0o755 },
  );

  return config;
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

run();
