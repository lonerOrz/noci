const { loadConfig } = require("./config");
const { ensureBinary } = require("./binary");
const { startProxy } = require("./proxy");
const { kill } = require("./proxy");
const utils = require("./utils");

async function run() {
  try {
    const actionRoot = require("path").resolve(__dirname, "../..");
    const config = loadConfig();
    const binPath = await ensureBinary(actionRoot);
    await startProxy(binPath, config.proxyPort, config.signingKey, config.noUpstream);
  } catch (error) {
    const failOnError = utils.getState("fail-on-error") === "true";
    if (failOnError) {
      utils.fail(error.message);
    } else {
      console.warn(
        `[noci-action] Setup warning: ${error.message}. Continuing without cache.`,
      );
    }
  }
}

process.on("SIGTERM", () => {
  const proxyPid = utils.getState("proxy-pid");
  kill(proxyPid);
  process.exit(0);
});

process.on("SIGINT", () => {
  const proxyPid = utils.getState("proxy-pid");
  kill(proxyPid);
  process.exit(0);
});

run();
