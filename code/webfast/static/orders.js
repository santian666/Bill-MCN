const detailState = {
  countries: [],
  settlementStatuses: [],
  dateRange: null,
  countryDateRanges: {},
  page: 1,
  pageSize: 100,
  totalPages: 0,
  lastQuery: new URLSearchParams()
};

const $ = (id) => document.getElementById(id);
const confirmState = { resolve: null, lastFocus: null };

function bindAutoScale() {
  const update = () => {
    const scale = Math.max(0.72, Math.min(1, window.innerWidth / 1366, window.innerHeight / 768));
    document.documentElement.style.setProperty("--ui-scale", scale.toFixed(3));
  };
  update();
  window.addEventListener("resize", update);
}

function toast(text) {
  const el = $("toast");
  el.textContent = text;
  el.style.display = "block";
  clearTimeout(window.__toastTimer);
  window.__toastTimer = setTimeout(() => el.style.display = "none", 2600);
}

async function api(url, options = {}) {
  const resp = await fetch(url, options);
  const data = await resp.json();
  if (!resp.ok) throw new Error(data.error || "请求失败");
  return data;
}

async function initOrdersPage() {
  const state = await api("/api/state");
  detailState.countries = state.countries || [];
  detailState.settlementStatuses = state.settlementStatuses || [];
  detailState.dateRange = state.dateRange || null;
  detailState.countryDateRanges = state.countryDateRanges || {};
  renderCountries();
  renderSettlementStatuses();
  applyURLParams();
  applyBillDateRange(false);
  await loadOrders();
}

function bindDatePickers() {
  document.querySelectorAll('input[type="date"]').forEach(input => {
    if (input.dataset.pickerBound) return;
    input.dataset.pickerBound = "1";
    input.addEventListener("click", () => openDatePicker(input));
  });
}

function openDatePicker(input) {
  input.focus();
  if (typeof input.showPicker === "function") {
    try {
      input.showPicker();
    } catch (_) {
      // 浏览器不允许重复打开时，保留默认点击行为。
    }
  }
}

function renderCountries() {
  $("detailCountry").innerHTML = detailState.countries.map(c => `<option value="${escapeHtml(c)}">${escapeHtml(c)}</option>`).join("");
}

function renderSettlementStatuses() {
  $("detailSettlementStatus").innerHTML = `<option value="">全部状态</option>` + detailState.settlementStatuses.map(s => `<option value="${escapeHtml(s)}">${escapeHtml(s)}</option>`).join("");
}

function applyURLParams() {
  const p = new URLSearchParams(location.search);
  if (p.get("country")) $("detailCountry").value = p.get("country");
  if (p.get("start")) $("detailStart").value = p.get("start");
  if (p.get("end")) $("detailEnd").value = p.get("end");
  if (p.get("settlementStatus")) $("detailSettlementStatus").value = p.get("settlementStatus");
  if (p.get("usernames")) $("detailUsers").value = p.get("usernames");
  if (["100", "500", "1000"].includes(p.get("pageSize"))) $("pageSize").value = p.get("pageSize");
  detailState.page = Number(p.get("page") || 1);
}

function currentDateRange() {
  const country = $("detailCountry").value;
  return detailState.countryDateRanges[country] || detailState.dateRange || {};
}

function applyBillDateRange(resetValues) {
  const range = currentDateRange();
  const start = $("detailStart");
  const end = $("detailEnd");
  if (!range.start || !range.end) return;
  start.min = range.start;
  start.max = range.end;
  end.min = range.start;
  end.max = range.end;
  if (resetValues || !start.value || start.value < range.start || start.value > range.end) start.value = range.start;
  if (resetValues || !end.value || end.value < range.start || end.value > range.end) end.value = range.end;
}

function buildQuery(includePage = true) {
  const p = new URLSearchParams();
  if ($("detailCountry").value) p.set("country", $("detailCountry").value);
  if ($("detailStart").value) p.set("start", $("detailStart").value);
  if ($("detailEnd").value) p.set("end", $("detailEnd").value);
  if ($("detailSettlementStatus").value) p.set("settlementStatus", $("detailSettlementStatus").value);
  const users = normalizeUsernames($("detailUsers").value).join(",");
  if (users) {
    p.set("scope", "manual");
    p.set("usernames", users);
  }
  if (includePage) {
    p.set("page", String(detailState.page));
    p.set("pageSize", $("pageSize").value);
  }
  return p;
}

function filterDescription() {
  const country = $("detailCountry").value || "未选择国家";
  const start = $("detailStart").value || "最早";
  const end = $("detailEnd").value || "最晚";
  const status = $("detailSettlementStatus").value || "全部状态";
  const users = normalizeUsernames($("detailUsers").value);
  const accountText = users.length ? `${users.length} 个账号` : "全部账号";
  return `国家：${country}\n时间：${start} 至 ${end}\n订单结算状态：${status}\n账号：${accountText}`;
}

async function loadOrders() {
  detailState.pageSize = Number($("pageSize").value);
  const query = buildQuery(true);
  detailState.lastQuery = query;
  $("searchOrdersBtn").disabled = true;
  $("searchOrdersBtn").textContent = "查询中...";
  try {
    const data = await api(`/api/orders?${query.toString()}`);
    detailState.page = data.page || 1;
    detailState.totalPages = data.totalPages || 0;
    const shown = (data.orders || []).length;
    $("ordersTotal").textContent = `共 ${data.total || 0} 条`;
    $("ordersShown").textContent = `当前显示：${shown} / ${data.total || 0}`;
    $("ordersIncome").textContent = `已结算 ${money(statusTotal(data.statusStats, "已结算"))}`;
    $("ordersExportCount").textContent = data.total || 0;
    $("ordersExportIncome").textContent = money(data.income || 0);
    $("ordersFilterRange").textContent = `时间范围：${$("detailStart").value || "最早"} 至 ${$("detailEnd").value || "最晚"}`;
    renderStatusStats(data.statusStats || []);
    $("ordersRange").textContent = `第 ${data.page || 0} / ${data.totalPages || 0} 页`;
    renderOrders(data.orders || []);
    updatePager();
    history.replaceState(null, "", `/orders?${query.toString()}`);
  } finally {
    $("searchOrdersBtn").disabled = false;
    $("searchOrdersBtn").textContent = "查询";
  }
}

function updatePager() {
  $("prevPageBtn").disabled = detailState.page <= 1;
  $("nextPageBtn").disabled = detailState.totalPages === 0 || detailState.page >= detailState.totalPages;
  renderPageButtons();
}

function renderPageButtons() {
  const wrap = $("pageButtons");
  const total = detailState.totalPages;
  if (!total) {
    wrap.innerHTML = "";
    return;
  }
  const pages = visiblePages(detailState.page, total);
  wrap.innerHTML = pages.map(page => {
    if (page === "...") return `<span class="page-ellipsis">...</span>`;
    const active = page === detailState.page ? " active" : "";
    return `<button class="page-btn${active}" data-page="${page}">${page}</button>`;
  }).join("");
}

function visiblePages(current, total) {
  if (total <= 9) return Array.from({ length: total }, (_, i) => i + 1);
  const pages = new Set([1, total, current, current - 1, current + 1, current - 2, current + 2]);
  const sorted = Array.from(pages).filter(p => p >= 1 && p <= total).sort((a, b) => a - b);
  const result = [];
  for (const page of sorted) {
    const prev = result[result.length - 1];
    if (prev && page - prev > 1) result.push("...");
    result.push(page);
  }
  return result;
}

function renderOrders(orders) {
  $("detailOrdersBody").innerHTML = orders.length ? orders.map(o => `
    <tr>
      ${detailCell(o.orderId)}
      ${detailCell(o.skuId)}
      ${detailCell(o.productId)}
      ${detailCell(o.productName)}
      ${detailCell(money(o.productPrice || 0))}
      ${detailCell(o.quantity || 0)}
      ${detailCell(o.settlementStatus)}
      ${detailCell(o.talentUsername)}
      ${detailCell(o.standardCommissionRate)}
      ${detailCell(o.shopAdCommissionRate)}
      ${detailCell(money(o.commissionGmv || 0))}
      ${detailCell(money(o.estimatedBaseCommission || 0))}
      ${detailCell(money(o.estimatedStandardCommission || 0))}
      ${detailCell(money(o.commissionBase || 0))}
      ${detailCell(money(o.standardCommission || 0))}
      ${detailCell(money(o.beforeSplitTalentIncome || 0))}
      ${detailCell(o.agencySplitRate)}
      ${detailCell(money(o.talentTotalIncome || 0))}
      ${detailCell(money(o.agencyPretaxTotalIncome || 0))}
      ${detailCell(money(o.agencyTotalIncome || 0))}
      ${detailCell(o.invitationId)}
      ${detailCell(o.createdAt || o.createdDate || "")}
      ${detailCell(o.deliveredAt)}
      ${detailCell(o.paymentId)}
      ${detailCell(o.paymentStatus)}
    </tr>
  `).join("") : `<tr><td colspan="25">暂无数据</td></tr>`;
}

function normalizeUsernames(text) {
  const seen = new Set();
  return String(text || "")
    .split(/[,\n\r，；;\t ]+/)
    .map(v => v.trim())
    .filter(Boolean)
    .filter(v => {
      const key = v.toLowerCase();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
}

function openTalentModal() {
  const names = normalizeUsernames($("detailUsers").value);
  $("talentModalText").value = names.join("\n");
  $("talentModal").classList.add("show");
  $("talentModal").setAttribute("aria-hidden", "false");
  $("talentModalText").focus();
}

function closeTalentModal() {
  $("talentModal").classList.remove("show");
  $("talentModal").setAttribute("aria-hidden", "true");
}

function confirmTalentModal() {
  const names = normalizeUsernames($("talentModalText").value);
  $("detailUsers").value = names.join(",");
  closeTalentModal();
  detailState.page = 1;
  loadOrders().catch(err => toast(err.message));
}

function openConfirmModal({ title, message, detail = "", confirmText = "确定" }) {
  return new Promise(resolve => {
    if (confirmState.resolve) {
      confirmState.resolve(false);
    }
    confirmState.resolve = resolve;
    confirmState.lastFocus = document.activeElement;
    $("confirmModalTitle").textContent = title || "确认操作";
    $("confirmModalMessage").textContent = message || "";
    $("confirmModalDetail").textContent = detail || "";
    $("confirmModalDetail").style.display = detail ? "block" : "none";
    $("confirmConfirmModalBtn").textContent = confirmText;
    $("confirmModal").classList.add("show");
    $("confirmModal").setAttribute("aria-hidden", "false");
    setTimeout(() => $("confirmConfirmModalBtn").focus(), 0);
  });
}

function closeConfirmModal(result) {
  const modal = $("confirmModal");
  if (!modal.classList.contains("show")) return;
  modal.classList.remove("show");
  modal.setAttribute("aria-hidden", "true");
  const resolve = confirmState.resolve;
  confirmState.resolve = null;
  if (resolve) resolve(Boolean(result));
  if (confirmState.lastFocus && typeof confirmState.lastFocus.focus === "function") {
    confirmState.lastFocus.focus();
  }
  confirmState.lastFocus = null;
}

function detailCell(value) {
  const text = String(value ?? "");
  const safe = escapeHtml(text);
  return `<td class="detail-cell" data-tip="${safe}">${safe}</td>`;
}

function renderStatusStats(items) {
  const stats = statusStatsMap(items);
  const statuses = detailState.settlementStatuses.length ? detailState.settlementStatuses : ["客户未付款", "待确认", "已结算", "不符合条件", "已冻结"];
  $("ordersStatusStats").innerHTML = statuses.map(status => {
    const item = stats[status] || { total: 0, orders: 0 };
    return `
      <div class="status-card">
        <span>${escapeHtml(status)}</span>
        <strong>${money(item.total || 0)}</strong>
        <em>${item.orders || 0} 单</em>
      </div>
    `;
  }).join("");
}

function statusStatsMap(items) {
  const map = {};
  for (const item of items || []) {
    map[item.status] = item;
  }
  return map;
}

function statusTotal(items, status) {
  const item = statusStatsMap(items)[status];
  return item ? item.total || 0 : 0;
}

function money(value) {
  const text = Number(value || 0).toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const symbol = currencySymbol(currentCountry());
  return symbol ? `${symbol} ${text}` : text;
}

function currentCountry() {
  const el = $("detailCountry");
  return el ? el.value : "";
}

function currencySymbol(country) {
  const map = {
    "新加坡": "S$",
    "泰国": "฿",
    "马来西亚": "RM",
    "马来": "RM",
    "菲律宾": "₱",
    "越南": "₫",
    "日本": "¥",
    "美国": "$",
    "巴西": "R$",
    "墨西哥": "Mex$",
    "英国": "£",
    "德国": "€",
    "法国": "€",
    "意大利": "€",
    "西班牙": "€"
  };
  return map[country] || "";
}

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, s => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[s]));
}

document.addEventListener("mousemove", (event) => {
  const cell = event.target.closest(".detail-cell");
  if (!cell) return;
  cell.style.setProperty("--tip-x", `${event.clientX + 12}px`);
  cell.style.setProperty("--tip-y", `${event.clientY + 12}px`);
});

$("searchOrdersBtn").addEventListener("click", () => {
  detailState.page = 1;
  loadOrders().catch(err => toast(err.message));
});
$("detailUsers").addEventListener("click", openTalentModal);
$("detailUsers").addEventListener("keydown", (event) => {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    openTalentModal();
  }
});
$("confirmTalentModalBtn").addEventListener("click", confirmTalentModal);
$("cancelTalentModalBtn").addEventListener("click", closeTalentModal);
$("closeTalentModalBtn").addEventListener("click", closeTalentModal);
$("clearTalentModalBtn").addEventListener("click", () => {
  $("talentModalText").value = "";
  $("talentModalText").focus();
});
$("talentModal").addEventListener("click", (event) => {
  if (event.target.id === "talentModal") closeTalentModal();
});
$("closeConfirmModalBtn").addEventListener("click", () => closeConfirmModal(false));
$("cancelConfirmModalBtn").addEventListener("click", () => closeConfirmModal(false));
$("confirmConfirmModalBtn").addEventListener("click", () => closeConfirmModal(true));
$("confirmModal").addEventListener("click", (event) => {
  if (event.target.id === "confirmModal") closeConfirmModal(false);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && $("confirmModal").classList.contains("show")) {
    closeConfirmModal(false);
    return;
  }
  if (event.key === "Escape" && $("talentModal").classList.contains("show")) {
    closeTalentModal();
  }
});
$("detailCountry").addEventListener("change", () => {
  applyBillDateRange(true);
  detailState.page = 1;
  loadOrders().catch(err => toast(err.message));
});
$("detailSettlementStatus").addEventListener("change", () => {
  detailState.page = 1;
  loadOrders().catch(err => toast(err.message));
});
$("pageSize").addEventListener("change", () => {
  detailState.page = 1;
  loadOrders().catch(err => toast(err.message));
});
$("prevPageBtn").addEventListener("click", () => {
  if (detailState.page > 1) {
    detailState.page--;
    loadOrders().catch(err => toast(err.message));
  }
});
$("nextPageBtn").addEventListener("click", () => {
  if (detailState.page < detailState.totalPages) {
    detailState.page++;
    loadOrders().catch(err => toast(err.message));
  }
});
$("pageButtons").addEventListener("click", (event) => {
  const btn = event.target.closest(".page-btn");
  if (!btn) return;
  detailState.page = Number(btn.dataset.page);
  loadOrders().catch(err => toast(err.message));
});
$("exportOrdersBtn").addEventListener("click", () => {
  window.location.href = `/api/export?${buildQuery(false).toString()}`;
});
$("deleteAllOrdersBtn").addEventListener("click", () => {
  deleteAllOrders().catch(err => toast(err.message));
});
$("deleteFilteredOrdersBtn").addEventListener("click", () => {
  deleteFilteredOrders().catch(err => toast(err.message));
});

async function deleteAllOrders() {
  const country = $("detailCountry").value;
  if (!country) {
    toast("请先选择国家");
    return;
  }
  const confirmed = await openConfirmModal({
    title: "删除当前国家订单",
    message: `确定删除 ${country} 的全部订单数据？`,
    detail: "其他国家的数据不会删除，员工和账号配置不会删除。",
    confirmText: "确定删除"
  });
  if (!confirmed) return;
  const secondConfirmed = await openConfirmModal({
    title: "再次确认",
    message: `${country} 的全部订单删除后不能在软件内恢复。`,
    detail: "只能重新导入账单恢复，请确认当前国家选择正确。",
    confirmText: "继续删除"
  });
  if (!secondConfirmed) return;
  const query = new URLSearchParams({ mode: "all", country });
  const data = await api(`/api/orders/delete?${query.toString()}`, { method: "DELETE" });
  toast(`已删除 ${data.deleted || 0} 条订单`);
  detailState.page = 1;
  await initOrdersPage();
}

async function deleteFilteredOrders() {
  const query = buildQuery(false);
  if (!query.get("country")) {
    toast("请先选择国家");
    return;
  }
  const desc = filterDescription();
  const confirmed = await openConfirmModal({
    title: "按条件删除订单",
    message: "确定按当前筛选条件删除订单？",
    detail: desc,
    confirmText: "确定删除"
  });
  if (!confirmed) return;
  const data = await api(`/api/orders/delete?mode=filter&${query.toString()}`, { method: "DELETE" });
  toast(`已删除 ${data.deleted || 0} 条订单`);
  detailState.page = 1;
  await initOrdersPage();
}

bindAutoScale();
bindDatePickers();
initOrdersPage().catch(err => toast(err.message));
