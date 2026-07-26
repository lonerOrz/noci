const cp = require("child_process");
const fs = require("fs");
const path = require("path");
const os = require("os");

const BINARY_PATH = "/tmp/noci";

async function ensureBinary(actionRoot) {
  if (fs.existsSync(BINARY_PATH)) return BINARY_PATH;

  return (
    findPrebuilt(actionRoot) ||
    (await downloadRelease()) ||
    (await buildFromSource(actionRoot))
  );
}

function findPrebuilt(actionRoot) {
  for (const p of [
    path.join(actionRoot, "action/noci"),
    path.join(actionRoot, "noci"),
    path.join(__dirname, "../noci"),
  ]) {
    if (fs.existsSync(p)) {
      console.log(`[noci-action] Using pre-built binary at ${p}`);
      link(p, BINARY_PATH);
      return BINARY_PATH;
    }
  }
  return null;
}

async function downloadRelease() {
  const repo = process.env.GITHUB_ACTION_REPOSITORY || "lonerOrz/noci";
  const version = process.env.GITHUB_ACTION_REF;
  const platform = os.platform();
  const arch = os.arch() === "x64" ? "amd64" : "arm64";
  const name = `noci-${platform}-${arch}`;
  const base = `https://github.com/${repo}/releases`;

  if (version) {
    const tag = version.startsWith("v") ? version : `v${version}`;
    try {
      await download(`${base}/download/${tag}/${name}`, BINARY_PATH);
      return BINARY_PATH;
    } catch {
      console.log(`[noci-action] Download ${tag} failed, trying latest...`);
    }
  }

  try {
    await download(`${base}/latest/download/${name}`, BINARY_PATH);
    return BINARY_PATH;
  } catch {
    console.log(
      "[noci-action] Release download failed, building from source...",
    );
    return null;
  }
}

async function buildFromSource(actionRoot) {
  const outLink = "/tmp/noci-result";
  if (fs.existsSync(outLink))
    fs.rmSync(outLink, { recursive: true, force: true });

  cp.execSync(`nix build "${actionRoot}" --out-link ${outLink}`, {
    stdio: "inherit",
    env: { ...process.env, NIX_IGNORE_HOME_DIRECTORY_ERROR: "1" },
  });

  const bin = path.join(outLink, "bin/noci");
  if (!fs.existsSync(bin))
    throw new Error("Build succeeded but binary not found at " + bin);
  link(bin, BINARY_PATH);
  return BINARY_PATH;
}

function link(src, dst) {
  try {
    fs.symlinkSync(src, dst);
  } catch {
    fs.copyFileSync(src, dst);
    fs.chmodSync(dst, "755");
  }
}

async function download(url, target) {
  const res = await fetch(url, { redirect: "follow" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const file = fs.createWriteStream(target);
  for await (const chunk of res.body) file.write(chunk);
  file.end();
  await new Promise((r) => file.on("finish", r));
  fs.chmodSync(target, "755");
}

module.exports = { ensureBinary, BINARY_PATH };
