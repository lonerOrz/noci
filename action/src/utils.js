const fs = require("fs");

function writeMultiline(filePath, key, value) {
  if (value.includes("\n")) {
    const delim = "NOCI_EOF";
    fs.appendFileSync(filePath, `${key}<<${delim}\n${value}\n${delim}\n`);
  } else {
    fs.appendFileSync(filePath, `${key}=${value}\n`);
  }
}

module.exports = {
  getSafeEnv(extras = {}) {
    return { ...process.env, NIX_IGNORE_HOME_DIRECTORY_ERROR: "1", ...extras };
  },

  getEnvOrInput(envKey, inputKey) {
    return (
      process.env[envKey] || process.env[`INPUT_${inputKey.toUpperCase()}`]
    );
  },

  saveState(key, value) {
    writeMultiline(process.env.GITHUB_STATE, `noci-state-${key}`, value);
  },

  exportVariable(key, value) {
    writeMultiline(process.env.GITHUB_ENV, key, value);
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
