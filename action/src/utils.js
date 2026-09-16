const fs = require("fs");
const crypto = require("crypto");

/**
 * Derive a Nix public key string from a Nix private key (key_name:base64).
 * Nix ed25519 private keys are either 64 bytes (seed+public) or 32 bytes (seed only).
 * Returns "" on invalid input so callers can fall back to HTTP fetch.
 */
function derivePublicKey(signingKey) {
  if (!signingKey) return "";
  const trimmed = signingKey.trim();
  const colonIdx = trimmed.indexOf(":");
  if (colonIdx === -1) return "";

  const keyName = trimmed.slice(0, colonIdx);
  const base64Secret = trimmed.slice(colonIdx + 1);
  try {
    const rawSecret = Buffer.from(base64Secret, "base64");
    let rawPublic;
    if (rawSecret.length === 64) {
      rawPublic = rawSecret.subarray(32);
    } else if (rawSecret.length === 32) {
      const keyObject = crypto.createPrivateKey({
        key: Buffer.concat([
          Buffer.from("302e020100300506032b657004220420", "hex"),
          rawSecret,
        ]),
        format: "der",
        type: "pkcs8",
      });
      const spki = crypto.createPublicKey(keyObject)
        .export({ format: "der", type: "spki" });
      rawPublic = spki.subarray(spki.length - 32);
    } else {
      return "";
    }
    return `${keyName}:${rawPublic.toString("base64")}`;
  } catch (e) {
    console.warn(`[noci-action] Failed to derive public key locally: ${e.message}`);
    return "";
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
    writeLine(process.env.GITHUB_STATE, `noci-state-${key}`, value);
  },

  getState(key) {
    return process.env[`STATE_noci-state-${key}`];
  },

  exportVariable(key, value) {
    process.env[key] = value;
    writeLine(process.env.GITHUB_ENV, key, value);
  },

  exportOutput(key, value) {
    fs.appendFileSync(process.env.GITHUB_OUTPUT, `${key}=${value}\n`);
  },

  fail(msg) {
    console.error(`[noci-action-error] ${msg}`);
    process.exit(1);
  },

  derivePublicKey,
};

