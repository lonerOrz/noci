const { loadConfig } = require("./config");
const { ensureBinary } = require("./binary");
const { startProxy } = require("./proxy");
const utils = require("./utils");

async function run() {
  try {
    const actionRoot = require("path").resolve(__dirname, "../..");
    const config = loadConfig();
    const binPath = await ensureBinary(actionRoot);
    await startProxy(binPath, config.proxyPort);
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

run();
