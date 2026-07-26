const utils = require("./utils");

function loadConfig() {
  const registry =
    utils.getEnvOrInput("NOCI_REGISTRY", "registry") || "ghcr.io";
  const repo =
    utils.getEnvOrInput("NOCI_REPO", "repo") || process.env.GITHUB_REPOSITORY;
  const token =
    utils.getEnvOrInput("NOCI_TOKEN", "token") ||
    process.env.GITHUB_TOKEN ||
    "";
  const signingKey =
    utils.getEnvOrInput("NOCI_SIGNING_KEY", "signing-key") || "";
  const proxyPort = utils.getEnvOrInput("NOCI_PROXY_PORT", "proxy-port") || "0";

  const compression =
    utils.getEnvOrInput("NOCI_COMPRESSION", "compression") || "zstd";
  const compressionLevel =
    utils.getEnvOrInput("NOCI_COMPRESSION_LEVEL", "compression-level") || "3";
  const jobs = utils.getEnvOrInput("NOCI_JOBS", "jobs") || "0";
  const skipUpstream =
    utils.getEnvOrInput("NOCI_SKIP_UPSTREAM", "skip-upstream") || "true";
  const failOnError =
    utils.getEnvOrInput("NOCI_FAIL_ON_ERROR", "fail-on-error") === "true";

  if (!repo) {
    throw new Error("Repository could not be determined.");
  }

  // Persist for post step
  utils.saveState("registry", registry);
  utils.saveState("repo", repo);
  utils.saveState("skip-upstream", skipUpstream);
  utils.saveState("compression", compression);
  utils.saveState("compression-level", compressionLevel);
  utils.saveState("jobs", jobs);
  utils.saveState("fail-on-error", failOnError ? "true" : "false");

  // Broadcast to subsequent steps
  utils.exportVariable("NOCI_REGISTRY", registry);
  utils.exportVariable("NOCI_REPO", repo);
  utils.exportVariable("NOCI_TOKEN", token);
  if (signingKey) utils.exportVariable("NOCI_SIGNING_KEY", signingKey);

  return {
    registry,
    repo,
    token,
    signingKey,
    proxyPort,
    compression,
    compressionLevel,
    jobs,
    skipUpstream,
    failOnError,
  };
}

module.exports = { loadConfig };
