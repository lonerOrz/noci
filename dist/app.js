let db = {};
let localDigest = "";
let entries = [];
let canDelete = false;

const proxyUrl = window.location.origin;
const container = document.getElementById("package-rows");
const searchInput = document.getElementById("search-input");

function updateStatus(isActive) {
  const badge = document.getElementById("status-badge");
  if (!badge) return;
  if (isActive) {
    badge.innerHTML = `
      <span class="inline-flex items-center rounded-full bg-emerald-500/10 px-3 py-1 text-xs font-semibold text-emerald-400 ring-1 ring-inset ring-emerald-500/20 select-none">
        <span class="w-1.5 h-1.5 mr-1.5 rounded-full bg-emerald-400 animate-pulse"></span>
        Active
      </span>
    `;
  } else {
    badge.innerHTML = `
      <span class="inline-flex items-center rounded-full bg-rose-500/10 px-3 py-1 text-xs font-semibold text-rose-400 ring-1 ring-inset ring-rose-500/20 select-none animate-pulse">
        <span class="w-1.5 h-1.5 mr-1.5 rounded-full bg-rose-400"></span>
        Offline
      </span>
    `;
  }
}

function updateStats(dataList) {
  const totalBytes = dataList.reduce((acc, cur) => acc + cur.nar_size, 0);
  document.getElementById("stat-count").innerText = dataList.length;
  document.getElementById("stat-size").innerText = formatBytes(totalBytes);
}

function rollbackUI(backupEntries, btn, originalText) {
  entries = backupEntries;
  render(searchInput.value);
  updateStats(entries);
  if (btn) {
    btn.disabled = false;
    btn.innerHTML = originalText;
  }
}

function initData(data) {
  db = data;
  canDelete = !!db.canDelete;

  const actionsHeader = document.getElementById("actions-header");
  if (actionsHeader) {
    actionsHeader.style.display = canDelete ? "table-cell" : "none";
  }

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

  updateStats(entries);
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
    const colspanVal = canDelete ? "5" : "4";
    container.innerHTML =
      `<tr><td colspan="${colspanVal}" class="py-10 text-center text-sm text-slate-500">No cached packages found</td></tr>`;
    return;
  }
  filtered.forEach((e, index) => {
    const date = new Date(e.added).toLocaleString();
    const staggerDelay = index * 30;

    let actionCell = "";
    if (canDelete) {
      actionCell =
        '<td class="whitespace-nowrap px-3 py-4 text-right pr-6 text-sm font-medium">' +
        `<button onclick="deletePackage(event, '${e.hash}', '${e.name}')" class="text-rose-400 hover:text-rose-300 select-none cursor-pointer btn-polish px-2 py-1 rounded">` +
        "Delete" +
        "</button>" +
        "</td>";
    }

    container.innerHTML +=
      `<tr class="hover:bg-slate-900/40 transition-colors duration-150 border-b border-slate-800/40 last:border-0 row-fade-in" style="animation-delay: ${staggerDelay}ms">` +
      '<td class="whitespace-nowrap py-4 pl-6 pr-3 text-sm font-semibold text-slate-200 select-text cursor-default">' +
      `<div class="max-w-[160px] sm:max-w-[260px] md:max-w-[360px] truncate" title="${e.name}">${e.name}</div>` +
      "</td>" +
      '<td class="whitespace-nowrap px-3 py-4 text-sm font-mono text-indigo-400 select-all cursor-pointer hover:text-indigo-300">' +
      `<div class="max-w-[110px] sm:max-w-[170px] md:max-w-[240px] truncate" title="${e.hash}">${e.hash}</div>` +
      "</td>" +
      '<td class="whitespace-nowrap px-3 py-4 text-sm text-slate-300 font-medium">' +
      formatBytes(e.nar_size) +
      "</td>" +
      '<td class="whitespace-nowrap px-3 py-4 text-sm text-slate-500">' +
      date +
      "</td>" +
      actionCell +
      "</tr>";
  });
}

async function checkUpdate() {
  try {
    const resDigest = await fetch("/api/digest");
    if (!resDigest.ok) {
      updateStatus(false);
      return;
    }
    const digest = await resDigest.text();
    updateStatus(true);

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
  } catch (err) {
    updateStatus(false);
  }
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
  updateStatus(false);
  try {
    const resDigest = await fetch("/api/digest");
    if (resDigest.ok) {
      localDigest = await resDigest.text();
    }
    const resIndex = await fetch("/api/index");
    if (resIndex.ok) {
      const data = await resIndex.json();
      initData(data);
      updateStatus(true);
    } else {
      updateStatus(false);
    }
  } catch (err) {
    updateStatus(false);
  }
  setInterval(checkUpdate, 3000);
}

searchInput.addEventListener("input", (e) => render(e.target.value));
window.addEventListener("DOMContentLoaded", init);
window.copyCmd = copyCmd;

async function deletePackage(event, hash, name) {
  event.stopPropagation();
  if (!confirm(`Are you sure you want to delete ${name} (${hash})?`)) {
    return;
  }

  const btn = event.currentTarget || event.target;
  const originalText = btn.innerHTML;
  btn.disabled = true;
  btn.innerHTML = "Deleting...";

  const backupEntries = [...entries];

  entries = entries.filter((e) => e.hash !== hash);
  render(searchInput.value);
  updateStats(entries);

  try {
    const res = await fetch(`/api/delete/${hash}`, {
      method: "DELETE",
    });

    if (res.ok) {
      await checkUpdate();
    } else {
      rollbackUI(backupEntries, btn, originalText);
      const errText = await res.text();
      alert(
        "Deletion failed: " +
          errText +
          "\n(Please verify if proxy token has delete/write permission)",
      );
    }
  } catch (err) {
    rollbackUI(backupEntries, btn, originalText);
    alert("Network error occurred during deletion");
  }
}

window.deletePackage = deletePackage;
