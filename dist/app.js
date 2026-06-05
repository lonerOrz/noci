let localDigest = "";
let entries = [];
let canDelete = false;

let currentPage = 1;
let pageSize = 50;
let totalItems = 0;
let searchTerm = "";
let isFetching = false;

const proxyUrl = window.location.origin;
const container = document.getElementById("package-rows");
const searchInput = document.getElementById("search-input");

function updateStatus(isActive) {
  const badge = document.getElementById("status-badge");
  if (!badge) return;
  if (isActive) {
    badge.innerHTML = `
      <div class="status-active">
        <span class="dot"></span>
        Active
      </div>
    `;
  } else {
    badge.innerHTML = `
      <div class="status-offline">
        <span class="dot"></span>
        Offline
      </div>
    `;
  }
}

function rollbackUI(backupEntries, btn, originalText) {
  entries = backupEntries;
  renderEntries();
  if (btn) {
    btn.disabled = false;
    btn.innerHTML = originalText;
  }
}

async function fetchPage() {
  if (isFetching) return;
  isFetching = true;
  try {
    const url = `/api/index?page=${currentPage}&limit=${pageSize}&search=${encodeURIComponent(searchTerm)}`;
    const res = await fetch(url);
    if (res.ok) {
      const data = await res.json();
      renderPage(data);
      updateStatus(true);
    } else {
      updateStatus(false);
    }
  } catch (err) {
    updateStatus(false);
  } finally {
    isFetching = false;
  }
}

function renderPage(data) {
  canDelete = !!data.canDelete;

  const actionsHeader = document.getElementById("actions-header");
  if (actionsHeader) {
    actionsHeader.style.display = canDelete ? "table-cell" : "none";
  }

  document.getElementById("repo-name").innerText =
    "OCI Repository: " + data.registry + "/" + data.repo;
  document.getElementById("stat-endpoint").innerText = data.registry;
  document.getElementById("nix-cmd").innerText =
    'nix build .#package --substituters "' +
    proxyUrl +
    '" --option require-sigs false';

  const displayCount =
    data.globalCount !== undefined ? data.globalCount : data.total;
  const displaySize =
    data.globalSize !== undefined
      ? data.globalSize
      : data.entries.reduce((acc, cur) => acc + cur.nar_size, 0);

  document.getElementById("stat-count").innerText = displayCount;
  document.getElementById("stat-size").innerText = formatBytes(displaySize);

  entries = data.entries || [];
  totalItems = data.total;

  renderEntries();
  renderPagination(data.total, data.page, data.limit);
}

function renderEntries() {
  container.innerHTML = "";
  if (entries.length === 0) {
    container.innerHTML = `<tr><td colspan="${canDelete ? 5 : 4}" class="empty-row">No cached packages found</td></tr>`;
    return;
  }
  entries.forEach((e, index) => {
    const date = new Date(e.added).toLocaleString();
    const staggerDelay = index * 30;

    let actionCell = "";
    if (canDelete) {
      actionCell = `
        <td>
          <button onclick="deletePackage(event, '${e.hash}', '${e.name}')" class="delete-btn">Delete</button>
        </td>
      `;
    }

    container.innerHTML += `
      <tr class="row-fade-in" style="animation-delay: ${staggerDelay}ms">
        <td>
          <div class="name-cell" title="${e.name}">${e.name}</div>
        </td>
        <td>
          <div class="hash-cell" title="${e.hash}">${e.hash}</div>
        </td>
        <td>${formatBytes(e.nar_size)}</td>
        <td>${date}</td>
        ${actionCell}
      </tr>
    `;
  });
}

function renderPagination(total, page, limit) {
  const maxPage = Math.ceil(total / limit) || 1;
  currentPage = page;

  let paginationContainer = document.getElementById("pagination-controls");
  if (!paginationContainer) {
    paginationContainer = document.createElement("div");
    paginationContainer.id = "pagination-controls";
    const table = container.closest("table");
    if (table && table.parentElement) {
      table.parentElement.parentElement.appendChild(paginationContainer);
    }
  }

  const startIdx = total === 0 ? 0 : (page - 1) * limit + 1;
  const endIdx = Math.min(page * limit, total);

  paginationContainer.innerHTML = `
    <div class="pagination-info">
      Showing ${startIdx} to ${endIdx} of ${total} packages
    </div>
    <div class="pagination-nav">
      <button onclick="changePage(${page - 1})" ${page <= 1 ? "disabled" : ""} class="pagination-btn">← Prev</button>
      <span class="pagination-text">Page ${page} of ${maxPage}</span>
      <button onclick="changePage(${page + 1})" ${page >= maxPage ? "disabled" : ""} class="pagination-btn">Next →</button>
    </div>
  `;
}

window.changePage = function (newPage) {
  currentPage = newPage;
  fetchPage();
};

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
      localDigest = digest;
      await fetchPage();
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
    btn.style.backgroundColor = "var(--accent-emerald)";
    btn.style.borderColor = "var(--accent-emerald)";
    btn.style.color = "white";
    setTimeout(() => {
      btn.innerText = originalText;
      btn.style.backgroundColor = "var(--bg-secondary)";
      btn.style.borderColor = "var(--border-normal)";
      btn.style.color = "var(--text-secondary)";
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
    await fetchPage();
  } catch (err) {
    updateStatus(false);
  }
  setInterval(checkUpdate, 3000);
}

let searchTimeout;
searchInput.addEventListener("input", (e) => {
  clearTimeout(searchTimeout);
  searchTimeout = setTimeout(() => {
    searchTerm = e.target.value;
    currentPage = 1;
    fetchPage();
  }, 250);
});

window.addEventListener("DOMContentLoaded", init);
window.copyCmd = copyCmd;

async function deletePackage(event, hash, name) {
  event.stopPropagation();
  if (!confirm(`Are you sure you want to delete ${name} (${hash})?`)) return;

  const btn = event.currentTarget || event.target;
  const originalText = btn.innerHTML;
  btn.disabled = true;
  btn.innerHTML = "Deleting...";

  const backupEntries = [...entries];
  entries = entries.filter((e) => e.hash !== hash);
  renderEntries();

  try {
    const res = await fetch(`/api/delete/${hash}`, { method: "DELETE" });
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
