const fs = require("fs");

module.exports = {
  getEnvOrInput(envKey, inputKey) {
    return (
      process.env[envKey] || process.env[`INPUT_${inputKey.toUpperCase()}`]
    );
  },

  saveState(key, value) {
    fs.appendFileSync(process.env.GITHUB_STATE, `noci-state-${key}=${value}\n`);
  },

  exportVariable(key, value) {
    fs.appendFileSync(process.env.GITHUB_ENV, `${key}=${value}\n`);
  },

  exportOutput(key, value) {
    fs.appendFileSync(process.env.GITHUB_OUTPUT, `${key}=${value}\n`);
  },

  fail(msg) {
    console.error(`[noci-action-error] ${msg}`);
    process.exit(1);
  },

  getState(key) {
    return process.env[`STATE_noci-state-${key}`];
  },
};
