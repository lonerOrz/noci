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
    const colspanVal = canDelete ? "5" : "4";
    container.innerHTML = `<tr><td colspan="${colspanVal}" class="py-10 text-center text-sm text-slate-500">No cached packages found</td></tr>`;
    return;
  }
  entries.forEach((e, index) => {
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

// 动态渲染并挂载分页控制器 UI 元素
function renderPagination(total, page, limit) {
  const maxPage = Math.ceil(total / limit) || 1;
  currentPage = page;

  let paginationContainer = document.getElementById("pagination-controls");
  if (!paginationContainer) {
    paginationContainer = document.createElement("div");
    paginationContainer.id = "pagination-controls";
    paginationContainer.className =
      "flex items-center justify-between border-t border-slate-800/60 px-6 py-4 bg-slate-900/10";
    const table = container.closest("table");
    if (table && table.parentElement) {
      table.parentElement.appendChild(paginationContainer);
    }
  }

  const startIdx = total === 0 ? 0 : (page - 1) * limit + 1;
  const endIdx = Math.min(page * limit, total);

  paginationContainer.innerHTML = `
    <div class="flex flex-1 justify-between sm:hidden">
      <button onclick="changePage(${page - 1})" ${page <= 1 ? "disabled" : ""} class="relative inline-flex items-center rounded-md border border-slate-700 bg-slate-800 px-4 py-2 text-sm font-medium text-slate-300 hover:bg-slate-700 disabled:opacity-50 select-none cursor-pointer">Previous</button>
      <button onclick="changePage(${page + 1})" ${page >= maxPage ? "disabled" : ""} class="relative ml-3 inline-flex items-center rounded-md border border-slate-700 bg-slate-800 px-4 py-2 text-sm font-medium text-slate-300 hover:bg-slate-700 disabled:opacity-50 select-none cursor-pointer">Next</button>
    </div>
    <div class="hidden sm:flex sm:flex-1 sm:items-center sm:justify-between">
      <div>
        <p class="text-sm text-slate-400">
          Showing <span class="font-medium text-slate-200">${startIdx}</span> to <span class="font-medium text-slate-200">${endIdx}</span> of <span class="font-medium text-slate-200">${total}</span> packages
        </p>
      </div>
      <div>
        <nav class="isolate inline-flex -space-x-px rounded-md shadow-sm" aria-label="Pagination">
          <button onclick="changePage(${page - 1})" ${page <= 1 ? "disabled" : ""} class="relative inline-flex items-center rounded-l-md px-3 py-2 text-slate-400 ring-1 ring-inset ring-slate-700 hover:bg-slate-800 focus:z-20 focus:outline-offset-0 disabled:opacity-30 cursor-pointer select-none">
            &larr; Prev
          </button>
          <span class="relative inline-flex items-center px-4 py-2 text-sm font-semibold text-slate-300 ring-1 ring-inset ring-slate-700 focus:outline-offset-0">
            Page ${page} of ${maxPage}
          </span>
          <button onclick="changePage(${page + 1})" ${page >= maxPage ? "disabled" : ""} class="relative inline-flex items-center rounded-r-md px-3 py-2 text-slate-400 ring-1 ring-inset ring-slate-700 hover:bg-slate-800 focus:z-20 focus:outline-offset-0 disabled:opacity-30 cursor-pointer select-none">
            Next &rarr;
          </button>
        </nav>
      </div>
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
      await fetchPage(); // 触发刷新当前分页数据
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
    await fetchPage();
  } catch (err) {
    updateStatus(false);
  }
  setInterval(checkUpdate, 3000);
}

// 模糊搜索输入防抖 (Debounce) 逻辑，防止高频触发数据库接口请求
let searchTimeout;
searchInput.addEventListener("input", (e) => {
  clearTimeout(searchTimeout);
  searchTimeout = setTimeout(() => {
    searchTerm = e.target.value;
    currentPage = 1; // 重新搜索时将页码重置回第 1 页
    fetchPage();
  }, 250);
});

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

  // 乐观更新：本地当前页剔除该条记录并重渲染
  entries = entries.filter((e) => e.hash !== hash);
  renderEntries();

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
