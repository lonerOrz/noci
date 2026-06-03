let db = {};
let localDigest = "";
let entries = [];

const currentPort = window.location.port || "8080";
const proxyUrl = "http://127.0.0.1:" + currentPort;
const container = document.getElementById("package-rows");
const searchInput = document.getElementById("search-input");

function initData(data) {
  db = data;
  document.getElementById("repo-name").innerText =
    "OCI Repository: " + db.registry + "/" + db.repo;
  document.getElementById("stat-endpoint").innerText = db.registry;
  document.getElementById("nix-cmd").innerText =
    'nix build .#package --substituters "' +
    proxyUrl +
    '" --option require-sigs false';

  entries = Object.keys(db.entries || {})
    .map((hash) => ({
      hash: hash,
      ...db.entries[hash],
    }))
    .sort((a, b) => b.added.localeCompare(a.added));

  const totalBytes = entries.reduce((acc, cur) => acc + cur.nar_size, 0);
  document.getElementById("stat-count").innerText = entries.length;
  document.getElementById("stat-size").innerText = formatBytes(totalBytes);
  render(searchInput.value);
}

function render(filterText) {
  container.innerHTML = "";
  const filtered = entries.filter((e) => {
    if (!filterText) return true;
    const q = filterText.toLowerCase();
    return e.name.toLowerCase().includes(q) || e.hash.toLowerCase().includes(q);
  });
  if (filtered.length === 0) {
    container.innerHTML =
      '<tr><td colspan="4" class="py-10 text-center text-sm text-slate-500">No cached packages found</td></tr>';
    return;
  }
  filtered.forEach((e) => {
    const date = new Date(e.added).toLocaleString();
    container.innerHTML +=
      '<tr class="hover:bg-slate-900/40 transition-colors border-b border-slate-800/40 last:border-0">' +
      '<td class="whitespace-nowrap py-4 pl-6 pr-3 text-sm font-semibold text-slate-200 select-text cursor-default">' +
      e.name +
      "</td>" +
      '<td class="whitespace-nowrap px-3 py-4 text-sm font-mono text-indigo-400 select-all cursor-pointer hover:text-indigo-300">' +
      e.hash +
      "</td>" +
      '<td class="whitespace-nowrap px-3 py-4 text-sm text-slate-300 font-medium">' +
      formatBytes(e.nar_size) +
      "</td>" +
      '<td class="whitespace-nowrap px-3 py-4 text-sm text-slate-500">' +
      date +
      "</td>" +
      "</tr>";
  });
}

async function checkUpdate() {
  try {
    const resDigest = await fetch("/api/digest");
    if (!resDigest.ok) return;
    const digest = await resDigest.text();
    if (!localDigest) {
      localDigest = digest;
      return;
    }
    if (digest && digest !== localDigest) {
      const resIndex = await fetch("/api/index");
      if (resIndex.ok) {
        const newDb = await resIndex.json();
        localDigest = digest;
        initData(newDb);
      }
    }
  } catch (err) {}
}

function formatBytes(bytes) {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
}

function copyCmd(event) {
  const cmdText = document.getElementById("nix-cmd").innerText;
  navigator.clipboard.writeText(cmdText).then(() => {
    const btn = event.currentTarget || event.target;
    const originalText = btn.innerText;
    btn.innerText = "Copied!";
    btn.classList.replace("bg-slate-800", "bg-emerald-600");
    setTimeout(() => {
      btn.innerText = originalText;
      btn.classList.replace("bg-emerald-600", "bg-slate-800");
    }, 1500);
  });
}

async function init() {
  try {
    const resDigest = await fetch("/api/digest");
    if (resDigest.ok) {
      localDigest = await resDigest.text();
    }
    const resIndex = await fetch("/api/index");
    if (resIndex.ok) {
      const data = await resIndex.json();
      initData(data);
    }
  } catch (err) {}
  setInterval(checkUpdate, 3000);
}

searchInput.addEventListener("input", (e) => render(e.target.value));
window.addEventListener("DOMContentLoaded", init);
window.copyCmd = copyCmd;
