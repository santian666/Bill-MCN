const appState = {
  countries: [],
  settlementStatuses: [],
  companies: [],
  talentDuplicates: {},
  dateRange: null,
  countryDateRanges: {},
  selected: { scope: "all", id: "", usernames: [] },
  checkedTree: {},
  collapsedCompanies: {},
  collapsedEmployees: {},
  menuTarget: null,
  inputHandler: null,
  lastQuery: "",
  hasStats: false,
  selectedBillFile: { path: "", name: "" },
  lastStatsData: null,
  importProgressTimer: null,
  importProgressSession: 0,
  importCancelRequested: false,
  activeImportRunId: 0,
  canceledImportRuns: {}
};

const $ = (id) => document.getElementById(id);
const confirmState = { resolve: null, lastFocus: null };
const STATS_RETURN_KEY = "billing.stats.returnState";

function bindAutoScale() {
  let lastFrame = "";
  const update = () => {
    const baseWidth = 1500;
    const baseHeight = 900;
    const viewportWidth = window.visualViewport ? window.visualViewport.width : window.innerWidth;
    const viewportHeight = window.visualViewport ? window.visualViewport.height : window.innerHeight;
    const scale = Math.max(0.18, Math.min(1.18, viewportWidth / baseWidth, viewportHeight / baseHeight));
    const frameWidth = Math.max(320, viewportWidth / scale);
    const frameHeight = Math.max(520, viewportHeight / scale);
    const nextScale = scale.toFixed(3);
    const nextFrame = `${nextScale}:${Math.round(frameWidth)}:${Math.round(frameHeight)}`;
    if (nextFrame === lastFrame) return;
    lastFrame = nextFrame;
    document.documentElement.style.setProperty("--design-width", `${frameWidth}px`);
    document.documentElement.style.setProperty("--design-height", `${frameHeight}px`);
    document.documentElement.style.setProperty("--ui-scale", nextScale);
  };
  update();
  window.addEventListener("resize", update);
  if (window.visualViewport) window.visualViewport.addEventListener("resize", update);
  setInterval(update, 300);
}

function uiScale() {
  const value = Number(getComputedStyle(document.documentElement).getPropertyValue("--ui-scale"));
  return value > 0 ? value : 1;
}

function toDesignPoint(event) {
  const scale = uiScale();
  return {
    x: event.clientX / scale,
    y: event.clientY / scale
  };
}

function designSize() {
  const styles = getComputedStyle(document.documentElement);
  return {
    width: Number.parseFloat(styles.getPropertyValue("--design-width")) || 1500,
    height: Number.parseFloat(styles.getPropertyValue("--design-height")) || 900
  };
}

function setStatus(text) {
  const btn = $("ordersPageBtn");
  if (btn) btn.title = text;
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
  const type = resp.headers.get("content-type") || "";
  const data = type.includes("application/json") ? await resp.json() : await resp.text();
  if (!resp.ok) throw new Error(data.error || "请求失败");
  return data;
}

async function loadState() {
  const selectedCountry = $("countrySelect").value;
  const selectedFilterCountry = $("filterCountry") ? $("filterCountry").value : "";
  const data = await api("/api/state");
  appState.countries = data.countries || [];
  appState.settlementStatuses = data.settlementStatuses || [];
  appState.companies = data.companies || [];
  appState.talentDuplicates = duplicateMap(data.talentDuplicates || []);
  appState.dateRange = data.dateRange || null;
  appState.countryDateRanges = data.countryDateRanges || {};
  pruneCheckedTree();
  renderCountries(selectedCountry, selectedFilterCountry);
  renderSettlementStatuses();
  applyBillDateRange();
  const restored = restoreStatsReturnState();
  if (!restored) clearStats();
  renderTree();
  setStatus(`已载入，订单 ${data.orderCount || 0} 条`);
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

function renderCountries(selectedCountry = "", selectedFilterCountry = "") {
  const html = appState.countries.map(c => `<option value="${escapeHtml(c)}">${escapeHtml(c)}</option>`).join("");
  $("countrySelect").innerHTML = html;
  if ($("filterCountry")) $("filterCountry").innerHTML = html;
  if ($("statCountry")) $("statCountry").innerHTML = html;
  if (selectedCountry && appState.countries.includes(selectedCountry)) {
    $("countrySelect").value = selectedCountry;
  }
  if (selectedFilterCountry && appState.countries.includes(selectedFilterCountry)) {
    $("filterCountry").value = selectedFilterCountry;
  } else if ($("filterCountry") && $("countrySelect").value) {
    $("filterCountry").value = $("countrySelect").value;
  }
}

function renderSettlementStatuses() {
  const html = `<option value="">全部状态</option>` + appState.settlementStatuses.map(s => `<option value="${escapeHtml(s)}">${escapeHtml(s)}</option>`).join("");
  $("settlementStatus").innerHTML = html;
  if ($("statSettlementStatus")) $("statSettlementStatus").innerHTML = html;
}

function currentDateRange() {
  const country = filterCountry();
  return appState.countryDateRanges[country] || appState.dateRange || {};
}

function filterCountry() {
  const el = $("filterCountry");
  return el ? el.value : $("countrySelect").value;
}

function applyBillDateRange() {
  const range = currentDateRange();
  const start = $("startDate");
  const end = $("endDate");
  if (!range.start || !range.end) return;
  start.min = range.start;
  start.max = range.end;
  end.min = range.start;
  end.max = range.end;
  if (!start.value || start.value < range.start || start.value > range.end) start.value = range.start;
  if (!end.value || end.value < range.start || end.value > range.end) end.value = range.end;
}

function renderTree() {
  const tree = $("tree");
  if (!appState.companies.length) {
    tree.innerHTML = `<div class="empty">暂无公司和员工，点击右上角新增公司</div>`;
    return;
  }
  tree.innerHTML = appState.companies.map(company => renderCompany(company)).join("");
}

function pruneCheckedTree() {
  const valid = {};
  for (const company of appState.companies) {
    valid[`company:${company.id}`] = true;
    for (const emp of company.employees || []) {
      valid[`employee:${emp.id}`] = true;
      for (const [index, name] of (emp.talents || []).entries()) {
        valid[`talent:${accountKey(company.id, emp.id, name, index)}`] = true;
      }
    }
  }
  for (const key of Object.keys(appState.checkedTree)) {
    if (!valid[key]) delete appState.checkedTree[key];
  }
}

function isCompanyCollapsed(companyId) {
  return appState.collapsedCompanies[companyId] !== false;
}

function isEmployeeCollapsed(employeeId) {
  return appState.collapsedEmployees[employeeId] !== false;
}

function renderCompany(company) {
  const collapsed = isCompanyCollapsed(company.id);
  const employees = company.employees || [];
  const accountTotal = employees.reduce((sum, emp) => sum + ((emp.talents || []).length), 0);
  return `
    <div class="tree-line company-line">
      <input class="tree-check" type="checkbox" ${isChecked("company", company.id) ? "checked" : ""} data-scope="company" data-id="${company.id}" data-name="${escapeHtml(company.name)}" title="勾选公司">
      <div class="tree-node ${isActive("company", company.id)}" data-scope="company" data-id="${company.id}" data-name="${escapeHtml(company.name)}">
        公司：${escapeHtml(company.name)}（${accountTotal}）
      </div>
      <button class="tree-action add-employee-btn" data-company="${company.id}" title="新增员工">+</button>
      <button class="tree-action text-action copy-company-btn" data-company="${company.id}" title="复制公司所有账号">复制</button>
      <button class="tree-action text-action danger-action delete-company-btn" data-company="${company.id}" title="删除公司">删除</button>
    </div>
    <div class="tree-child ${collapsed ? "hidden" : ""}">
      ${employees.length ? employees.map(emp => renderEmployee(company.id, emp)).join("") : `<div class="empty">暂无员工</div>`}
    </div>
  `;
}

function renderEmployee(companyId, emp) {
  const collapsed = isEmployeeCollapsed(emp.id);
  const accounts = emp.talents || [];
  return `
    <div class="tree-line employee-line">
      <input class="tree-check" type="checkbox" ${isChecked("employee", emp.id) ? "checked" : ""} data-scope="employee" data-id="${emp.id}" data-company="${companyId}" data-name="${escapeHtml(emp.name)}" title="勾选员工">
      <div class="tree-node ${isActive("employee", emp.id)}" data-scope="employee" data-id="${emp.id}" data-company="${companyId}" data-name="${escapeHtml(emp.name)}">
        员工：${escapeHtml(emp.name)}（${accounts.length}）
      </div>
      <button class="tree-action add-account-btn" data-company="${companyId}" data-employee="${emp.id}" title="新增账号">+</button>
      <button class="tree-action text-action copy-employee-btn" data-company="${companyId}" data-employee="${emp.id}" title="复制员工账号">复制</button>
      <button class="tree-action text-action danger-action delete-employee-btn" data-company="${companyId}" data-employee="${emp.id}" title="删除员工">删除</button>
    </div>
    <div class="tree-child ${collapsed ? "hidden" : ""}">
      ${accounts.length ? accounts.map((name, index) => renderAccount(companyId, emp.id, name, index)).join("") : `<div class="empty">暂无账号</div>`}
    </div>
  `;
}

function renderAccount(companyId, employeeId, name, index) {
  const duplicateText = duplicateAccountText(companyId, employeeId, name);
  const duplicateClass = duplicateText ? " duplicate-account" : "";
  const duplicateTag = duplicateText ? `<span class="duplicate-tag" title="${escapeHtml(duplicateText)}">${escapeHtml(duplicateText)}</span>` : "";
  const key = accountKey(companyId, employeeId, name, index);
  return `
    <div class="tree-line account-line">
      <input class="tree-check" type="checkbox" ${isChecked("talent", key) ? "checked" : ""} data-scope="talent" data-id="${escapeHtml(key)}" data-company="${companyId}" data-employee="${employeeId}" data-index="${index}" data-user="${escapeHtml(name)}" data-name="${escapeHtml(name)}" title="勾选账号">
      <div class="tree-node account-node${duplicateClass} ${isActive("talent", key)}" data-scope="talent" data-id="${escapeHtml(key)}" data-company="${companyId}" data-employee="${employeeId}" data-index="${index}" data-user="${escapeHtml(name)}" data-name="${escapeHtml(name)}">
        <span class="account-name">${escapeHtml(name)}</span>${duplicateTag}
      </div>
    </div>
  `;
}

function isActive(scope, id) {
  return appState.selected.scope === scope && appState.selected.id === id ? "active" : "";
}

function isChecked(scope, id) {
  return !!appState.checkedTree[`${scope}:${id}`];
}

function accountKey(companyId, employeeId, name, index = "") {
  return `${companyId}|${employeeId}|${index}|${name}`;
}

function duplicateMap(items) {
  const map = {};
  for (const item of items || []) {
    const username = String(item.username || "").trim();
    if (!username) continue;
    map[username.toLowerCase()] = item.refs || [];
  }
  return map;
}

function duplicateAccountText(companyId, employeeId, name) {
  const refs = appState.talentDuplicates[String(name || "").trim().toLowerCase()] || [];
  const others = refs.filter(ref => ref.companyId !== companyId || ref.employeeId !== employeeId);
  if (!others.length && refs.length < 2) return "";
  const sourceRefs = others.length ? others : refs;
  const labels = uniqueNames(sourceRefs.map(ref => `${ref.companyName || "未知公司"} / ${ref.employeeName || "未知员工"}`));
  return labels.length ? `和 ${labels.slice(0, 2).join("、")} 重复` : "重复账号";
}

function currentParams() {
  const p = new URLSearchParams();
  const country = filterCountry();
  if (country) p.set("country", country);
  if ($("startDate").value) p.set("start", $("startDate").value);
  if ($("endDate").value) p.set("end", $("endDate").value);
  if ($("settlementStatus").value) p.set("settlementStatus", $("settlementStatus").value);
  p.set("scope", appState.selected.scope);
  if (appState.selected.id) p.set("id", appState.selected.id);
  if (appState.selected.usernames.length) p.set("usernames", appState.selected.usernames.join(","));
  return p;
}

function clearStats() {
	appState.hasStats = false;
	appState.lastQuery = "";
	appState.lastStatsData = null;
  $("resultTitle").textContent = "筛选结果";
  $("resultHint").textContent = "";
  $("totalIncome").textContent = "";
  $("orderCount").textContent = "";
  $("talentCount").textContent = "";
  $("exportOrderCount").textContent = "";
  $("exportIncome").textContent = "";
	renderStatusStats([]);
  updateAgencySettlement();
	$("summaryBody").innerHTML = `<tr><td colspan="14"></td></tr>`;
	setExportButtonsDisabled(true);
	saveStatsReturnState();
}

async function runStats() {
  if (!filterCountry()) {
    clearStats();
    toast("请先选择国家");
    return;
  }
  const params = currentParams();
  appState.lastQuery = params.toString();
  showStatsProgress();
  setStatsButtonsLoading(true);
  setStatus("正在统计...");
  try {
    const data = await api(`/api/stats?${appState.lastQuery}`);
    appState.hasStats = true;
    renderStats(data);
    setStatus("统计完成");
    saveStatsReturnState();
  } finally {
    setStatsButtonsLoading(false);
    hideStatsProgress();
  }
}

function showStatsProgress() {
  const modal = $("statsProgressModal");
  if (!modal) return;
  const selected = appState.selected && appState.selected.usernames ? appState.selected.usernames.length : 0;
  const sub = $("statsProgressSubTitle");
  if (sub) sub.textContent = selected ? `正在统计 ${selected} 个账号` : "正在计算筛选结果";
  modal.classList.add("show");
  modal.setAttribute("aria-hidden", "false");
}

function hideStatsProgress() {
  const modal = $("statsProgressModal");
  if (!modal) return;
  modal.classList.remove("show");
  modal.setAttribute("aria-hidden", "true");
}

function setStatsButtonsLoading(loading) {
  for (const id of ["manualSearchBtn", "statCheckedBtn", "confirmStatModalBtn"]) {
    const btn = $(id);
    if (!btn) continue;
    btn.disabled = loading;
    if (loading) {
      btn.dataset.oldText = btn.textContent;
      btn.textContent = "统计中...";
    } else if (btn.dataset.oldText) {
      btn.textContent = btn.dataset.oldText;
      delete btn.dataset.oldText;
    }
  }
}

function renderStats(data) {
  appState.lastStatsData = data;
  $("resultTitle").textContent = "筛选结果";
  $("resultHint").textContent = filterText(data);
  $("totalIncome").textContent = money(statusTotal(data.statusStats, "已结算"));
  $("orderCount").textContent = data.orderCount || 0;
  $("talentCount").textContent = data.talentCount || 0;
  $("exportOrderCount").textContent = data.orderCount || 0;
  $("exportIncome").textContent = money(data.total || 0);
  setExportButtonsDisabled(false);
  renderStatusStats(data.statusStats || []);
  updateAgencySettlement();
  const perTalent = data.perTalent || [];
  $("summaryBody").innerHTML = perTalent.length ? perTalent.map(item => `
    <tr data-user="${escapeHtml(item.username)}" class="summary-row">
      <td><button class="summary-account" type="button" data-user="${escapeHtml(item.username)}" title="查看此账号订单明细">${escapeHtml(item.username)}</button></td>
      ${summaryMetricCell(talentStatusTotal(item, "已结算"), talentStatusOrders(item, "已结算"), "is-settled")}
      ${summaryMetricCell(talentStatusTotal(item, "机构未付款"), talentStatusOrders(item, "机构未付款"), "is-agency-unpaid")}
      ${summaryMetricCell(talentStatusTotal(item, "客户未付款"), talentStatusOrders(item, "客户未付款"), "")}
      ${summaryMetricCell(talentStatusTotal(item, "待确认"), talentStatusOrders(item, "待确认"), "")}
      ${summaryMetricCell(talentStatusTotal(item, "不符合条件"), talentStatusOrders(item, "不符合条件"), "")}
      ${summaryMetricCell(talentStatusTotal(item, "已冻结"), talentStatusOrders(item, "已冻结"), "")}
      <td class="total-orders-cell"><span class="total-orders-pill">${item.orders || 0} 单</span></td>
    </tr>
  `).join("") : `<tr><td colspan="8" class="empty-cell">暂无数据</td></tr>`;
}

function summaryMetricCell(amount, count, extraClass) {
  const amt = Number(amount || 0);
  const cnt = Number(count || 0);
  if (amt === 0 && cnt === 0) {
    return `<td><div class="metric-inline is-empty" title="0.00 · 0 单">—</div></td>`;
  }
  const zeroAmtClass = amt === 0 ? " is-zero-amount" : "";
  const cls = [extraClass, zeroAmtClass].filter(Boolean).join(" ");
  return `<td><div class="metric-inline ${cls}"><span class="cell-amount">${money(amt)}</span><span class="cell-count">${cnt}单</span></div></td>`;
}

function setExportButtonsDisabled(disabled) {
  for (const id of ["exportSummaryBtn", "exportOrdersBtn", "exportBothBtn"]) {
    const btn = $(id);
    if (btn) btn.disabled = disabled;
  }
}

function exportCurrent(type) {
  if (!appState.hasStats || !appState.lastQuery) {
    toast("请先统计筛选结果");
    return;
  }
  const query = new URLSearchParams(appState.lastQuery);
  query.set("type", type);
  appendAgencySettlementExportParams(query);
  downloadFile(`/api/export?${query.toString()}`, currentExportFilename()).catch(err => toast(err.message));
}

function currentExportFilename() {
  const settled = statusTotal((appState.lastStatsData || {}).statusStats || [], "已结算");
  const amount = money(settled);
  let prefix = "";
  if (appState.selected.scope === "employee") {
    prefix = (findEmployee(appState.selected.id) || {}).name || "";
  } else if (appState.selected.scope === "company") {
    prefix = (findCompany(appState.selected.id) || {}).name || "";
  }
  return `${sanitizeFilename(prefix)}${sanitizeFilename(amount)}.xlsx`;
}

function sanitizeFilename(value) {
  return String(value || "").replace(/[<>:"/\\|?*\x00-\x1f]/g, "_").trim();
}

function appendAgencySettlementExportParams(query) {
  [
    ["usdRate", "usdRate"],
    ["rmbRate", "rmbRate"],
    ["agencyCutRate", "agencyCutRate"],
    ["feePermille", "feePermille"]
  ].forEach(([inputId, queryKey]) => {
    const el = $(inputId);
    const value = el ? String(el.value || "").trim() : "";
    const numeric = Number(value);
    if (value && Number.isFinite(numeric) && numeric > 0) query.set(queryKey, value);
  });
}

async function downloadFile(url, preferredFilename = "") {
  const resp = await fetch(url);
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(text || "导出失败");
  }
  const blob = await resp.blob();
  const filename = preferredFilename || exportFilename(resp.headers.get("content-disposition")) || "export.xlsx";
  const objectUrl = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = objectUrl;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(objectUrl), 1000);
  toast("导出已生成");
}

function exportFilename(disposition) {
  const text = disposition || "";
  const match = text.match(/filename="?([^"]+)"?/i);
  return match ? match[1] : "";
}

function openTalentOrders(username) {
  username = String(username || "").trim();
  if (!username) return;
  saveStatsReturnState();
  const query = new URLSearchParams(appState.lastQuery || currentParams().toString());
  query.set("scope", "manual");
  query.delete("id");
  query.set("usernames", username);
  query.set("page", "1");
  if (!query.get("pageSize")) query.set("pageSize", "100");
  window.location.href = `/orders?${query.toString()}`;
}

function saveStatsReturnState() {
	const summary = $("summaryBody") ? $("summaryBody").closest(".summary") : null;
	const tree = $("tree");
  const state = {
    savedAt: Date.now(),
    controls: {
      country: $("countrySelect").value,
      filterCountry: $("filterCountry") ? $("filterCountry").value : "",
      start: $("startDate").value,
      end: $("endDate").value,
      settlementStatus: $("settlementStatus").value,
      manualUsers: $("manualUsers").value
    },
    app: {
      selected: appState.selected,
      checkedTree: appState.checkedTree,
      collapsedCompanies: appState.collapsedCompanies,
      collapsedEmployees: appState.collapsedEmployees,
      lastQuery: appState.lastQuery,
      hasStats: appState.hasStats,
      lastStatsData: appState.lastStatsData
    },
    scroll: {
      summaryTop: summary ? summary.scrollTop : 0,
      summaryLeft: summary ? summary.scrollLeft : 0,
      treeTop: tree ? tree.scrollTop : 0
    }
  };
  try {
    sessionStorage.setItem(STATS_RETURN_KEY, JSON.stringify(state));
  } catch (_) {
    // 会话存储不可用时，保留正常跳转，不影响查询。
  }
}

function restoreStatsReturnState() {
  let state = null;
	try {
		const raw = sessionStorage.getItem(STATS_RETURN_KEY);
		if (!raw) return false;
		sessionStorage.removeItem(STATS_RETURN_KEY);
		state = JSON.parse(raw);
	} catch (_) {
		return false;
	}
	if (!state || !state.app) return false;
	const controls = state.controls || {};
  if (controls.country && appState.countries.includes(controls.country)) $("countrySelect").value = controls.country;
  if (controls.filterCountry && $("filterCountry") && appState.countries.includes(controls.filterCountry)) $("filterCountry").value = controls.filterCountry;
  if (controls.start) $("startDate").value = controls.start;
  if (controls.end) $("endDate").value = controls.end;
  $("settlementStatus").value = controls.settlementStatus || "";
  $("manualUsers").value = controls.manualUsers || "";

  appState.selected = state.app.selected || { scope: "all", id: "", usernames: [] };
  appState.checkedTree = state.app.checkedTree || {};
  appState.collapsedCompanies = state.app.collapsedCompanies || {};
	appState.collapsedEmployees = state.app.collapsedEmployees || {};
	appState.lastQuery = state.app.lastQuery || "";
	appState.hasStats = !!state.app.hasStats;
	appState.lastStatsData = state.app.lastStatsData || null;
	if (appState.hasStats && appState.lastStatsData) {
		renderStats(appState.lastStatsData);
		setExportButtonsDisabled(false);
	} else {
		clearStats();
	}
  setTimeout(() => {
    const summary = $("summaryBody") ? $("summaryBody").closest(".summary") : null;
    const tree = $("tree");
    const scroll = state.scroll || {};
    if (summary) {
      summary.scrollTop = scroll.summaryTop || 0;
      summary.scrollLeft = scroll.summaryLeft || 0;
    }
    if (tree) tree.scrollTop = scroll.treeTop || 0;
  }, 0);
  return true;
}

function filterText(data) {
  const scope = data.title || "全部订单";
  const country = filterCountry() || "全部国家";
  const start = $("startDate").value || "最早";
  const end = $("endDate").value || "最晚";
  const status = $("settlementStatus").value || "全部状态";
  return `已选择的筛选条件：${scope}，国家：${country}，时间：${start} 至 ${end}，订单结算状态：${status}`;
}

function statusCardClass(status) {
  switch (status) {
    case "已结算": return "status-settled";
    case "机构未付款": return "status-agency-unpaid";
    case "客户未付款": return "status-customer-unpaid";
    case "待确认": return "status-pending";
    case "不符合条件": return "status-ineligible";
    case "已冻结": return "status-frozen";
    default: return "";
  }
}

function renderStatusStats(items) {
  const stats = statusStatsMap(items);
  const statuses = appState.settlementStatuses.length ? appState.settlementStatuses : ["客户未付款", "待确认", "已结算", "机构未付款", "不符合条件", "已冻结"];
  $("statusStats").innerHTML = statuses.map(status => {
    const item = stats[status] || { total: 0, orders: 0 };
    return `
      <div class="status-card ${statusCardClass(status)}">
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

function talentStatusTotal(item, status) {
  return item && item.statusTotals ? item.statusTotals[status] || 0 : 0;
}

function talentStatusOrders(item, status) {
  return item && item.statusOrders ? item.statusOrders[status] || 0 : 0;
}

function parseOptionalNumber(id) {
  const el = $(id);
  if (!el) return null;
  const text = String(el.value || "").trim();
  if (!text) return null;
  const value = Number(text);
  return Number.isFinite(value) ? value : null;
}

function roundCents(value) {
  const num = Number(value || 0);
  if (!Number.isFinite(num) || num === 0) return 0;
  const rounded = Math.round((num + Math.sign(num) * 1e-9) * 100) / 100;
  return rounded === 0 ? 0 : rounded;
}

function updateAgencySettlement() {
  const stats = appState.lastStatsData ? appState.lastStatsData.statusStats : [];
  const settled = roundCents(statusTotal(stats, "已结算"));
  const usdRate = parseOptionalNumber("usdRate");
  const rmbRate = parseOptionalNumber("rmbRate");
  const cutInput = validOptionalPositive("agencyCutRate");
  const feePermille = validFeePermille();
  const cutRate = cutInput == null ? 1 : Math.max(0, (100 - cutInput) / 100);
  let base = settled;
  if (usdRate != null) base *= usdRate;
  if (rmbRate != null) base *= rmbRate;
  const beforeFee = roundCents(base * cutRate);
  const fee = roundCents(beforeFee * feePermille / 1000);
  const actual = roundCents(beforeFee - fee);
  if ($("agencyBase")) $("agencyBase").textContent = money(settled);
  if ($("agencyFee")) $("agencyFee").textContent = rmbMoney(fee);
  if ($("agencyActual")) $("agencyActual").textContent = rmbMoney(actual);
}

function validFeePermille() {
  const value = validOptionalPositive("feePermille");
  return value == null ? 0 : value;
}

function validOptionalPositive(id) {
  const value = parseOptionalNumber(id);
  return value != null && value > 0 ? value : null;
}

function rmbMoney(value) {
  const text = Number(value || 0).toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  return `￥ ${text}`;
}

async function importBill() {
  const desktopFile = appState.selectedBillFile || { path: "", name: "" };
  const file = $("billFile").files[0];
  if (!desktopFile.path && !file) {
    await openImportResultModal("账单导入失败", "请选择账单文件。", "", "primary");
    return;
  }
  const country = $("countrySelect").value;
  const fileName = desktopFile.name || (file ? file.name : "");
  if (!country) {
    await openImportResultModal("账单导入失败", "请先选择国家。", `文件：${fileName}`, "primary");
    return;
  }
  $("importBtn").disabled = true;
  $("importBtn").textContent = "导入中...";
  const importRunId = ++appState.activeImportRunId;
  appState.importCancelRequested = false;
  try {
    showImportProgress("正在导入账单", country, fileName, "正在分析账单");
    let data = desktopFile.path
      ? await uploadBillPath(desktopFile.path, country, false)
      : await uploadBill(file, false);
    stopImportProgressPolling();
    if (data.requiresConfirm) {
      if (isImportRunCanceled(importRunId)) return;
      hideImportProgress();
      const confirmed = await openTextConfirm("导入更新确认", importConfirmText(data), "覆盖更新");
      if (!confirmed) {
        toast("已取消导入更新");
        return;
      }
      $("importBtn").textContent = "更新中...";
      showImportProgress("正在更新重复订单", country, fileName, "只覆盖有变化的订单");
      data = desktopFile.path
        ? await uploadBillPath(desktopFile.path, country, true)
        : await uploadBill(file, true);
      stopImportProgressPolling();
    }
    if (isImportRunCanceled(importRunId)) return;
    hideImportProgress();
    await openImportResultModal(
      "结果",
      "账单导入成功",
      `国家：${country}\n文件：${fileName}\n新增：${data.imported || 0} 条\n覆盖更新：${data.updated || 0} 条`,
      "primary"
    );
    loadState().catch(err => toast(err.message));
  } catch (err) {
    stopImportProgressPolling();
    if (isImportRunCanceled(importRunId)) return;
    await openImportResultModal("账单导入失败", err.message || "导入失败，请检查账单文件。", `国家：${country}\n文件：${fileName}`, "primary");
  } finally {
    hideImportProgress();
    $("importBtn").disabled = false;
    $("importBtn").textContent = "导入账单";
  }
}

function showImportProgress(title, country, filename, message) {
  const modal = $("importProgressModal");
  if (!modal) return;
  appState.importProgressSession += 1;
  modal.dataset.session = String(appState.importProgressSession);
  const labels = [
    ["importProgressProcessed", "已处理"],
    ["importProgressImported", "新增"],
    ["importProgressUpdated", "更新"],
    ["importProgressSkipped", "跳过无变化"],
    ["importProgressDuplicate", "重复订单"],
    ["importProgressChanged", "有变化"]
  ];
  for (const [id, text] of labels) {
    const node = $(id);
    if (node && node.previousElementSibling) node.previousElementSibling.textContent = text;
  }
  ensureImportCancelButton();
  const cancelBtn = $("cancelImportBtn");
  if (cancelBtn) {
    cancelBtn.disabled = false;
    cancelBtn.textContent = "中断导入";
  }
  $("importProgressTitle").textContent = title || "正在导入账单";
  $("importProgressSubTitle").textContent = `${country || ""} ${filename || ""}`.trim();
  updateImportProgress({
    active: true,
    phase: "准备中",
    country,
    filename,
    processed: 0,
    imported: 0,
    updated: 0,
    skippedUnchanged: 0,
    duplicateCount: 0,
    changedDuplicate: 0,
    message: message || "正在处理，请不要关闭软件。"
  });
  modal.classList.add("show");
  modal.setAttribute("aria-hidden", "false");
  startImportProgressPolling(appState.importProgressSession);
}

function ensureImportCancelButton() {
  const modal = $("importProgressModal");
  if (!modal || $("cancelImportBtn")) return;
  const target = modal.querySelector(".import-progress-modal") || modal.querySelector(".modal");
  if (!target) return;
  const actions = document.createElement("div");
  actions.className = "modal-actions import-progress-actions";
  const btn = document.createElement("button");
  btn.id = "cancelImportBtn";
  btn.type = "button";
  btn.className = "ghost danger";
  btn.textContent = "中断导入";
  btn.addEventListener("click", cancelImport);
  actions.appendChild(btn);
  target.appendChild(actions);
}

async function cancelImport() {
  appState.importCancelRequested = true;
  appState.canceledImportRuns[appState.activeImportRunId] = true;
  stopImportProgressPolling();
  hideImportProgress();
  const importBtn = $("importBtn");
  if (importBtn) {
    importBtn.disabled = false;
    importBtn.textContent = "导入账单";
  }
  const btn = $("cancelImportBtn");
  if (btn) {
    btn.disabled = true;
    btn.textContent = "正在中断...";
  }
  try {
    const app = desktopApp();
    api("/api/import/cancel", { method: "POST" }).catch(apiErr => {
      if (app && typeof app.CancelImport === "function") {
        return app.CancelImport();
      }
      throw apiErr;
    }).catch(err => toast(err.message || "中断导入失败"));
    setImportCancelButtonText("已请求中断");
  } catch (err) {
    setImportCancelButtonText("中断失败");
    toast(err.message || "中断导入失败");
  }
}

function isImportRunCanceled(runId) {
  return !!appState.canceledImportRuns[runId];
}

function setImportCancelButtonText(text) {
  const btn = $("cancelImportBtn");
  if (btn) btn.textContent = text;
}

function hideImportProgress() {
  stopImportProgressPolling();
  const modal = $("importProgressModal");
  if (!modal) return;
  modal.classList.remove("show");
  modal.setAttribute("aria-hidden", "true");
}

function startImportProgressPolling(session) {
  stopImportProgressPolling();
  appState.importProgressTimer = setInterval(() => {
    readImportProgress().then(progress => updateImportProgress(progress, session)).catch(() => {});
  }, 350);
}

function stopImportProgressPolling() {
  if (appState.importProgressTimer) {
    clearInterval(appState.importProgressTimer);
    appState.importProgressTimer = null;
  }
}

async function readImportProgress() {
  const app = desktopApp();
  if (app && typeof app.ImportProgress === "function") {
    return app.ImportProgress();
  }
  return api("/api/import/progress");
}

function updateImportProgress(progress = {}, session = appState.importProgressSession) {
  const setText = (id, value) => {
    const el = $(id);
    if (el) el.textContent = value;
  };
  setText("importProgressPhase", progress.phase || "处理中");
  setText("importProgressProcessed", formatCount(progress.processed));
  setText("importProgressImported", formatCount(progress.imported));
  setText("importProgressUpdated", formatCount(progress.updated));
  setText("importProgressSkipped", formatCount(progress.skippedUnchanged));
  setText("importProgressDuplicate", formatCount(progress.duplicateCount));
  setText("importProgressChanged", formatCount(progress.changedDuplicate));
  setText("importProgressMessage", progress.message || "正在处理，请不要关闭软件。");
  const bar = $("importProgressBar");
  if (bar) {
    const total = Number(progress.total || 0);
    const processed = Number(progress.processed || 0);
    if (total > 0) {
      const percent = Math.max(2, Math.min(100, Math.round(processed * 100 / total)));
      bar.style.width = `${percent}%`;
      bar.classList.add("determinate");
      bar.textContent = `${percent}%`;
    } else {
      bar.style.width = "";
      bar.classList.remove("determinate");
      bar.textContent = "";
    }
  }
  const sub = $("importProgressSubTitle");
  if (sub && (progress.country || progress.filename)) {
    sub.textContent = `${progress.country || ""} ${progress.filename || ""}`.trim();
  }
  if (progress.active === false && session === appState.importProgressSession) {
    window.setTimeout(() => hideImportProgress(), 250);
  }
}

function formatCount(value) {
  return Number(value || 0).toLocaleString("zh-CN");
}

function openImportResultModal(title, message, detail = "", intent = "primary") {
  return openConfirmModal({
    title,
    message,
    detail,
    confirmText: "知道了",
    intent,
    showCancel: false
  });
}

function uploadBill(file, confirmUpdate) {
  const fd = new FormData();
  fd.append("country", $("countrySelect").value);
  fd.append("file", file);
  if (confirmUpdate) fd.append("confirmUpdate", "1");
  return api("/api/import", { method: "POST", body: fd });
}

function desktopApp() {
  return window.go && window.go.main && window.go.main.App ? window.go.main.App : null;
}

function uploadBillPath(path, country, confirmUpdate) {
  const app = desktopApp();
  if (!app || typeof app.ImportBillFile !== "function") {
    throw new Error("桌面导入接口未就绪，请重新启动软件。");
  }
  return app.ImportBillFile(path, country, !!confirmUpdate);
}

function setSelectedBillFile(path, name) {
  appState.selectedBillFile = { path: path || "", name: name || "" };
  const label = $("selectedBillFileName");
  if (!label) return;
  label.textContent = name || "未选择文件";
  label.title = path || name || "未选择文件";
}

function basename(path) {
  return String(path || "").split(/[\\/]/).pop();
}

async function chooseBillFile() {
  const app = desktopApp();
  if (app && typeof app.SelectBillFile === "function") {
    const path = await app.SelectBillFile();
    if (!path) return;
    $("billFile").value = "";
    setSelectedBillFile(path, basename(path));
    return;
  }
  $("billFile").click();
}

function importConfirmText(data) {
  const lines = [
    `发现 ${data.duplicateCount || 0} 条重复订单。`,
    "确认后会先删除这些旧订单，再写入本次账单里的新数据。",
    "是否继续覆盖更新？"
  ];
  const fields = data.changedFields || [];
  if (fields.length) {
    lines.push("", "发生变化的字段：");
    for (const item of fields.slice(0, 12)) {
      lines.push(`- ${item.field}：${item.count} 条`);
    }
    if (fields.length > 12) lines.push(`- 还有 ${fields.length - 12} 个字段...`);
  }
  const samples = data.samples || [];
  if (samples.length) {
    lines.push("", "示例订单变化：");
    for (const sample of samples.slice(0, 5)) {
      const changes = (sample.changes || []).slice(0, 4).map(change => `${change.field}：${shortText(change.old)} -> ${shortText(change.new)}`).join("；");
      lines.push(`- ${sample.key}：${changes}`);
    }
    if (samples.length > 5) lines.push(`- 还有 ${samples.length - 5} 条示例未显示...`);
  }
  return lines.join("\n");
}

function shortText(value) {
  const text = String(value ?? "").trim();
  if (!text) return "空";
  return text.length > 28 ? `${text.slice(0, 28)}...` : text;
}

function openInputModal(title, placeholder, value, handler) {
  $("inputModalTitle").textContent = title;
  $("inputModalText").placeholder = placeholder || "一行一个";
  $("inputModalText").value = value || "";
  appState.inputHandler = handler;
  $("inputModal").classList.add("show");
  $("inputModal").setAttribute("aria-hidden", "false");
  $("inputModalText").focus();
}

function closeInputModal() {
  $("inputModal").classList.remove("show");
  $("inputModal").setAttribute("aria-hidden", "true");
  appState.inputHandler = null;
}

function openStatModal() {
  bindDatePickers();
  const checkedCount = Object.keys(appState.checkedTree).length;
  if (!checkedCount) {
    toast("请先勾选公司、员工或账号");
    return;
  }
  $("statModalTitle").textContent = checkedCount ? `统计筛选（已勾选 ${checkedCount} 项）` : "统计筛选";
  $("statStartDate").value = $("startDate").value;
  $("statEndDate").value = $("endDate").value;
  $("statSettlementStatus").value = $("settlementStatus").value;
  if ($("statCountry")) $("statCountry").value = filterCountry();
  applyStatDateRange();
  $("statModal").classList.add("show");
  $("statModal").setAttribute("aria-hidden", "false");
}

function closeStatModal() {
  $("statModal").classList.remove("show");
  $("statModal").setAttribute("aria-hidden", "true");
}

function openConfirmModal({ title, message, detail = "", confirmText = "确定", cancelText = "取消", intent = "danger", showCancel = true }) {
  return new Promise(resolve => {
    if (confirmState.resolve) {
      confirmState.resolve(false);
    }
    confirmState.resolve = resolve;
    confirmState.lastFocus = document.activeElement;
    const modal = $("confirmModal");
    const confirmBtn = $("confirmConfirmModalBtn");
    const cancelBtn = $("cancelConfirmModalBtn");
    $("confirmModalTitle").textContent = title || "确认操作";
    $("confirmModalMessage").textContent = message || "";
    $("confirmModalDetail").textContent = detail || "";
    $("confirmModalDetail").style.display = detail ? "block" : "none";
    confirmBtn.textContent = confirmText;
    cancelBtn.textContent = cancelText;
    confirmBtn.className = intent === "primary" ? "primary confirm-primary" : "danger-solid";
    cancelBtn.style.display = showCancel ? "" : "none";
    modal.classList.toggle("confirm-danger", intent !== "primary");
    modal.classList.toggle("confirm-info", intent === "primary");
    modal.classList.add("show");
    modal.setAttribute("aria-hidden", "false");
    setTimeout(() => confirmBtn.focus(), 0);
  });
}

function closeConfirmModal(result) {
  const modal = $("confirmModal");
  if (!modal.classList.contains("show")) return;
  modal.classList.remove("show");
  modal.setAttribute("aria-hidden", "true");
  const resolve = confirmState.resolve;
  $("cancelConfirmModalBtn").textContent = "取消";
  confirmState.resolve = null;
  if (resolve) resolve(Boolean(result));
  if (confirmState.lastFocus && typeof confirmState.lastFocus.focus === "function") {
    confirmState.lastFocus.focus();
  }
  confirmState.lastFocus = null;
}

function openTextConfirm(title, text, confirmText = "确定", intent = "danger") {
  const lines = String(text || "").split(/\r?\n/);
  const message = lines.shift() || "";
  const detail = lines.join("\n").trim();
  return openConfirmModal({ title, message, detail, confirmText, intent });
}

function applyStatDateRange() {
  const range = currentDateRange();
  const start = $("statStartDate");
  const end = $("statEndDate");
  if (!range.start || !range.end) return;
  start.min = range.start;
  start.max = range.end;
  end.min = range.start;
  end.max = range.end;
  if (!start.value || start.value < range.start || start.value > range.end) start.value = range.start;
  if (!end.value || end.value < range.start || end.value > range.end) end.value = range.end;
}

async function confirmStatModal() {
  $("startDate").value = $("statStartDate").value;
  $("endDate").value = $("statEndDate").value;
  $("settlementStatus").value = $("statSettlementStatus").value;
  if ($("filterCountry") && $("statCountry")) $("filterCountry").value = $("statCountry").value;
  applyCheckedStats();
  closeStatModal();
  renderTree();
  await runStats();
}

async function confirmInputModal() {
  const text = $("inputModalText").value.trim();
  if (!text) return toast("请输入内容");
  const handler = appState.inputHandler;
  if (!handler) return;
  const button = $("confirmInputModalBtn");
  const originalText = button.textContent;
  button.disabled = true;
  button.textContent = "保存中...";
  try {
    await handler(text);
    closeInputModal();
  } finally {
    button.disabled = false;
    button.textContent = originalText;
  }
}

function addCompany() {
  openInputModal("新增公司", "一行一个公司名称", "", async (text) => {
    const names = splitInputLines(text);
    const data = await api("/api/company", { method: "POST", headers: jsonHeader(), body: JSON.stringify({ name: names.join("\n") }) });
    const companies = data.companies || [];
    const skipped = data.skipped || [];
    const lastCompany = companies[companies.length - 1];
    if (lastCompany) {
      appState.selected = { scope: "company", id: lastCompany.id, usernames: [] };
      appState.collapsedCompanies[lastCompany.id] = true;
    }
    await loadState();
    if (!companies.length) throw new Error(`公司已存在：${skipped.join("、")}`);
    toast(skipped.length ? `新增 ${companies.length} 个公司，跳过 ${skipped.length} 个重名公司` : `已新增 ${companies.length} 个公司`);
  });
}

function addEmployee(companyId) {
  if (!companyId) return toast("请先选择公司");
  openInputModal("新增员工", "一行一个员工名称", "", async (text) => {
    const data = await api("/api/employee", { method: "POST", headers: jsonHeader(), body: JSON.stringify({ companyId, name: text }) });
    const employees = data.employees || [];
    const skipped = data.skipped || [];
    if (employees.length) {
      const last = employees[employees.length - 1];
      appState.selected = { scope: "employee", id: last.id, usernames: [] };
      appState.collapsedEmployees[last.id] = true;
    }
    await loadState();
    if (!employees.length) throw new Error(`员工已存在：${skipped.join("、")}`);
    toast(skipped.length ? `新增 ${employees.length} 个员工，跳过 ${skipped.length} 个重名员工` : `已新增 ${employees.length} 个员工`);
  });
}

function addAccount(companyId, employeeId) {
  if (!companyId || !employeeId) return toast("请先选择员工");
  openInputModal("新增账号", "一行一个达人账号", "", async (text) => {
    const companyCollapsed = isCompanyCollapsed(companyId);
    const employeeCollapsed = isEmployeeCollapsed(employeeId);
    const accountText = splitAccountLines(text).join("\n");
    let data = await saveAccounts(companyId, employeeId, accountText, false);
    if (data.requiresConfirm) {
      const duplicateNames = (data.duplicates || []).map(item => item.username);
      const confirmed = await openConfirmModal({
        title: "重复账号确认",
        message: `发现 ${data.skipped || duplicateNames.length} 个重复账号，是否继续重复添加？`,
        detail: duplicateConfirmDetail(data),
        confirmText: "继续添加",
        cancelText: "只导入不重复账号",
        intent: "primary"
      });
      if (confirmed && duplicateNames.length) {
        data = await saveAccounts(companyId, employeeId, duplicateNames.join("\n"), true);
      } else {
        data.message = `已跳过重复账号，新增 ${data.added || 0} 个未重复账号`;
      }
    }
    appState.selected = { scope: "employee", id: employeeId, usernames: [] };
    await loadState();
    appState.collapsedCompanies[companyId] = companyCollapsed;
    appState.collapsedEmployees[employeeId] = employeeCollapsed;
    renderTree();
    toast(data.message || "账号已新增");
  });
}

function saveAccounts(companyId, employeeId, usernames, confirmDuplicate) {
  return api("/api/talent", {
    method: "POST",
    headers: jsonHeader(),
    body: JSON.stringify({ companyId, employeeId, usernames, confirmDuplicate })
  });
}

function duplicateConfirmText(data) {
  const lines = [
    `发现 ${data.skipped || (data.duplicates || []).length} 个重复账号。`,
    data.added ? `未重复的 ${data.added} 个账号已直接新增。` : "",
    "是否继续重复添加这些账号？",
    ""
  ].filter(Boolean);
  for (const item of (data.duplicates || []).slice(0, 12)) {
    const refs = (item.refs || [])
      .map(ref => `${ref.companyName || "未知公司"} / ${ref.employeeName || "未知员工"}`)
      .join("、");
    lines.push(`- ${item.username}：已存在于 ${refs}`);
  }
  if ((data.duplicates || []).length > 12) {
    lines.push(`- 还有 ${(data.duplicates || []).length - 12} 个重复账号未显示`);
  }
  return lines.join("\n");
}

function duplicateConfirmDetail(data) {
  const lines = [];
  if (data.added) {
    lines.push(`未重复的 ${data.added} 个账号已直接新增。`);
    lines.push("");
  }
  for (const item of (data.duplicates || []).slice(0, 16)) {
    const refs = (item.refs || [])
      .map(ref => `${ref.companyName || "未知公司"} / ${ref.employeeName || "未知员工"}`)
      .join("、");
    lines.push(`${item.username}：已存在于 ${refs || "未知位置"}`);
  }
  if ((data.duplicates || []).length > 16) {
    lines.push(`还有 ${(data.duplicates || []).length - 16} 个重复账号未显示。`);
  }
  lines.push("");
  lines.push("继续添加后，重复账号会在树状列表中标红，并显示重复来源标签。");
  return lines.filter(line => line !== null && line !== undefined).join("\n");
}

async function editTreeItem(target) {
  hideTreeMenu();
  if (!target) return;
  if (target.scope === "company") {
    openInputModal("编辑公司", "公司名称", target.name, async (text) => {
      await api("/api/company", { method: "PUT", headers: jsonHeader(), body: JSON.stringify({ id: target.id, name: firstLine(text) }) });
      await loadState();
      toast("公司已修改");
    });
  } else if (target.scope === "employee") {
    openInputModal("编辑员工", "员工名称", target.name, async (text) => {
      await api("/api/employee", { method: "PUT", headers: jsonHeader(), body: JSON.stringify({ companyId: target.companyId, employeeId: target.id, name: firstLine(text) }) });
      await loadState();
      toast("员工已修改");
    });
  } else if (target.scope === "talent") {
    openInputModal("编辑账号", "账号名称", target.name, async (text) => {
      const newName = firstAccountLine(text);
      let data = await renameAccount(target, newName, false);
      if (data && data.requiresConfirm) {
        const confirmed = await openConfirmModal({
          title: "重复账号确认",
          message: "改动后的账号已存在重复，是否继续修改？",
          detail: duplicateConfirmDetail(data),
          confirmText: "继续修改",
          cancelText: "取消修改",
          intent: "primary"
        });
        if (!confirmed) return;
        data = await renameAccount(target, newName, true);
      }
      appState.selected = { scope: "talent", id: newName, usernames: [newName] };
      await loadState();
      toast("账号已修改");
    });
  }
}

function renameAccount(target, newName, confirmDuplicate) {
  return api("/api/talent", {
    method: "PUT",
    headers: jsonHeader(),
    body: JSON.stringify({
      companyId: target.companyId,
      employeeId: target.employeeId,
      oldName: target.name,
      newName,
      confirmDuplicate
    })
  });
}

async function deleteTreeItem(target) {
  hideTreeMenu();
  if (!target) return;
  const label = target.scope === "company" ? "公司" : target.scope === "employee" ? "员工" : "账号";
  const confirmed = await openConfirmModal({
    title: `删除${label}`,
    message: `确定删除${label}：${target.name}？`,
    detail: "删除后不能在软件内恢复。",
    confirmText: "确定删除"
  });
  if (!confirmed) return;
  if (target.scope === "company") {
    await api("/api/company", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ id: target.id }) });
  } else if (target.scope === "employee") {
    await api("/api/employee", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ companyId: target.companyId, employeeId: target.id }) });
  } else if (target.scope === "talent") {
    await api("/api/talent", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ companyId: target.companyId, employeeId: target.employeeId, name: target.name, index: parseOptionalIndex(target.index) }) });
  }
  appState.selected = { scope: "all", id: "", usernames: [] };
  await loadState();
  toast(`${label}已删除`);
}

async function deleteCheckedItems() {
  const checkedItems = checkedItemsFromDOM();
  if (!checkedItems.length) {
    toast("请先勾选要删除的公司、员工或账号");
    return;
  }
  const plan = buildDeleteCheckedPlan(checkedItems);
  const detail = [
    plan.companies.length ? `公司 ${plan.companies.length} 个` : "",
    plan.employees.length ? `员工 ${plan.employees.length} 个` : "",
    plan.talents.length ? `账号 ${plan.talents.length} 个` : ""
  ].filter(Boolean).join("，");
  const confirmed = await openConfirmModal({
    title: "删除勾选项",
    message: `确定删除已勾选的${detail}？`,
    detail: "删除后不能在软件内恢复。",
    confirmText: "确定删除"
  });
  if (!confirmed) return;
  for (const company of plan.companies) {
    await api("/api/company", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ id: company.id }) });
  }
  for (const employee of plan.employees) {
    await api("/api/employee", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ companyId: employee.companyId, employeeId: employee.id }) });
  }
  for (const item of plan.talents) {
    await api("/api/talent", {
      method: "DELETE",
      headers: jsonHeader(),
      body: JSON.stringify({ companyId: item.companyId, employeeId: item.employeeId, name: item.name || item.user, index: parseOptionalIndex(item.index) })
    });
  }
  appState.checkedTree = {};
  appState.selected = { scope: "all", id: "", usernames: [] };
  clearStats();
  await loadState();
  toast("勾选项已删除");
}

function checkedItemsFromDOM() {
  const items = [];
  document.querySelectorAll("#tree .tree-check:checked").forEach(check => {
    const scope = check.dataset.scope || "";
    if (scope === "company") {
      items.push({ scope, id: check.dataset.id || "", name: check.dataset.name || "" });
    } else if (scope === "employee") {
      items.push({
        scope,
        id: check.dataset.id || "",
        companyId: check.dataset.company || "",
        name: check.dataset.name || ""
      });
    } else if (scope === "talent") {
      items.push({
        scope,
        id: check.dataset.id || "",
        companyId: check.dataset.company || "",
        employeeId: check.dataset.employee || "",
        index: check.dataset.index || "",
        user: check.dataset.user || "",
        name: check.dataset.name || ""
      });
    }
  });
  appState.checkedTree = {};
  for (const item of items) {
    if (item.scope === "company") {
      appState.checkedTree[`company:${item.id}`] = item;
    } else if (item.scope === "employee") {
      appState.checkedTree[`employee:${item.id}`] = item;
    } else if (item.scope === "talent") {
      const key = `talent:${item.id || accountKey(item.companyId, item.employeeId, item.user, item.index)}`;
      appState.checkedTree[key] = item;
    }
  }
  return items;
}

function buildDeleteCheckedPlan(items) {
  const companyIds = new Set(items.filter(item => item.scope === "company").map(item => item.id));
  const employeeIds = new Set();
  const companies = items.filter(item => item.scope === "company");
  const employees = [];
  for (const item of items) {
    if (item.scope !== "employee" || companyIds.has(item.companyId)) continue;
    employeeIds.add(item.id);
    employees.push(item);
  }
  const talents = items
    .filter(item => item.scope === "talent" && !companyIds.has(item.companyId) && !employeeIds.has(item.employeeId))
    .sort((a, b) => {
      const group = `${a.companyId}|${a.employeeId}`.localeCompare(`${b.companyId}|${b.employeeId}`);
      if (group !== 0) return group;
      return Number(b.index || 0) - Number(a.index || 0);
    });
  return { companies, employees, talents };
}

function parseOptionalIndex(value) {
  if (value === "" || value === undefined || value === null) return undefined;
  const n = Number(value);
  return Number.isInteger(n) ? n : undefined;
}

function hasCheckedItems() {
  return Object.keys(appState.checkedTree).length > 0;
}

async function copyCompanyAccounts(companyId) {
  const company = findCompany(companyId);
  if (!company) return toast("公司不存在");
  const accounts = companyAccounts(company);
  await copyAccounts(accounts, `已复制公司 ${company.name} 的 ${accounts.length} 个账号`);
}

async function copyEmployeeAccounts(companyId, employeeId) {
  const employee = findEmployeeInCompany(companyId, employeeId);
  if (!employee) return toast("员工不存在");
  const accounts = employee.talents || [];
  await copyAccounts(accounts, `已复制员工 ${employee.name} 的 ${accounts.length} 个账号`);
}

async function copyAccounts(accounts, message) {
  const names = uniqueNames(accounts);
  if (!names.length) return toast("没有可复制的账号");
  const text = names.join("\n");
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else {
      fallbackCopyText(text);
    }
    toast(message);
  } catch (_) {
    fallbackCopyText(text);
    toast(message);
  }
}

function fallbackCopyText(text) {
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "readonly");
  area.style.position = "fixed";
  area.style.left = "-9999px";
  document.body.appendChild(area);
  area.select();
  document.execCommand("copy");
  area.remove();
}

async function deleteCompanyDirect(companyId) {
  const company = findCompany(companyId);
  if (!company) return toast("公司不存在");
  const count = companyAccounts(company).length;
  const confirmed = await openConfirmModal({
    title: "删除公司",
    message: `确定删除公司：${company.name}？`,
    detail: `将同时删除 ${count} 个账号记录。\n删除后不能在软件内恢复。`,
    confirmText: "确定删除"
  });
  if (!confirmed) return;
  await api("/api/company", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ id: companyId }) });
  appState.selected = { scope: "all", id: "", usernames: [] };
  clearStats();
  await loadState();
  toast("公司已删除");
}

async function deleteEmployeeDirect(companyId, employeeId) {
  const employee = findEmployeeInCompany(companyId, employeeId);
  if (!employee) return toast("员工不存在");
  const count = (employee.talents || []).length;
  const confirmed = await openConfirmModal({
    title: "删除员工",
    message: `确定删除员工：${employee.name}？`,
    detail: `将同时删除 ${count} 个账号记录。\n删除后不能在软件内恢复。`,
    confirmText: "确定删除"
  });
  if (!confirmed) return;
  await api("/api/employee", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ companyId, employeeId }) });
  appState.selected = { scope: "all", id: "", usernames: [] };
  clearStats();
  await loadState();
  toast("员工已删除");
}

function selectTreeNode(node) {
  const scope = node.dataset.scope;
  if (scope === "company") {
    appState.collapsedCompanies[node.dataset.id] = !isCompanyCollapsed(node.dataset.id);
    return false;
  } else if (scope === "employee") {
    appState.collapsedEmployees[node.dataset.id] = !isEmployeeCollapsed(node.dataset.id);
    return false;
  } else if (scope === "talent") {
    const checked = !isChecked("talent", node.dataset.id);
    setAccountChecked({
      companyId: node.dataset.company || "",
      employeeId: node.dataset.employee || "",
      index: node.dataset.index || "",
      user: node.dataset.user || "",
      name: node.dataset.name || ""
    }, checked);
    if (!checked) {
      delete appState.checkedTree[`employee:${node.dataset.employee}`];
      delete appState.checkedTree[`company:${node.dataset.company}`];
    }
    return true;
  }
  return false;
}

function showTreeMenu(event, node) {
  event.preventDefault();
  appState.menuTarget = {
    scope: node.dataset.scope,
    id: node.dataset.id,
    name: node.dataset.name || node.dataset.user || "",
    companyId: node.dataset.company || "",
    employeeId: node.dataset.employee || "",
    index: node.dataset.index || ""
  };
  const menu = $("treeMenu");
  const point = toDesignPoint(event);
  const size = designSize();
  menu.style.left = `${Math.min(point.x, size.width - 120)}px`;
  menu.style.top = `${Math.min(point.y, size.height - 86)}px`;
  menu.classList.add("show");
}

function hideTreeMenu() {
  $("treeMenu").classList.remove("show");
}

function selectedCompanyID() {
  if (appState.selected.scope === "company") return appState.selected.id;
  if (appState.selected.scope === "employee") {
    for (const c of appState.companies) {
      if ((c.employees || []).some(e => e.id === appState.selected.id)) return c.id;
    }
  }
  return "";
}

function jsonHeader() {
  return { "Content-Type": "application/json" };
}

function splitInputLines(text) {
  const seen = new Set();
  return String(text || "")
    .split(/[\n\r,，;；\t]+/)
    .map(v => v.trim())
    .filter(Boolean)
    .filter(v => {
      const key = v.toLowerCase();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
}

function normalizeAccountName(value) {
  return String(value || "").trim().toLowerCase();
}

function splitAccountLines(text) {
  const seen = new Set();
  return String(text || "")
    .split(/[\n\r,，;；\t ]+/)
    .map(normalizeAccountName)
    .filter(Boolean)
    .filter(v => {
      if (seen.has(v)) return false;
      seen.add(v);
      return true;
    });
}

function firstLine(text) {
  return splitInputLines(text)[0] || "";
}

function firstAccountLine(text) {
  return splitAccountLines(text)[0] || "";
}

function applyCheckedStats() {
  const checkedItems = Object.values(appState.checkedTree);
  if (!checkedItems.length) {
    appState.selected = { scope: "all", id: "", usernames: [] };
    return;
  }
  const checkedCompanies = checkedItems.filter(item => item.scope === "company");
  if (checkedCompanies.length === 1 && checkedItems.every(item => item.scope === "company" || item.companyId === checkedCompanies[0].id)) {
    appState.selected = { scope: "company", id: checkedCompanies[0].id, usernames: [] };
    return;
  }
  const checkedEmployees = checkedItems.filter(item => item.scope === "employee");
  if (checkedEmployees.length === 1 && checkedCompanies.length === 0 && checkedItems.every(item => item.scope === "employee" || item.employeeId === checkedEmployees[0].id)) {
    appState.selected = { scope: "employee", id: checkedEmployees[0].id, usernames: [] };
    return;
  }
  const names = [];
  const seen = new Set();
  for (const item of checkedItems) {
    for (const name of namesForCheckedItem(item)) {
      const key = name.toLowerCase();
      if (!seen.has(key)) {
        seen.add(key);
        names.push(name);
      }
    }
  }
  appState.selected = names.length ? { scope: "manual", id: "", usernames: names } : { scope: "all", id: "", usernames: [] };
}

function toggleTreeCheck(check) {
  const checked = check.checked;
  const scope = check.dataset.scope;
  if (scope === "company") {
    if (checked) appState.checkedTree = {};
    const company = appState.companies.find(c => c.id === check.dataset.id);
    if (company) setCompanyChecked(company, checked);
  } else if (scope === "employee") {
    if (checked) appState.checkedTree = {};
    const employee = findEmployee(check.dataset.id);
    if (employee) setEmployeeChecked(check.dataset.company, employee, checked);
    if (!checked) delete appState.checkedTree[`company:${check.dataset.company}`];
  } else if (scope === "talent") {
    if (checked && Object.values(appState.checkedTree).some(item => item.scope === "company" || item.scope === "employee")) {
      appState.checkedTree = {};
    }
    setAccountChecked({
      companyId: check.dataset.company || "",
      employeeId: check.dataset.employee || "",
      index: check.dataset.index || "",
      user: check.dataset.user || "",
      name: check.dataset.name || ""
    }, checked);
    if (!checked) {
      delete appState.checkedTree[`employee:${check.dataset.employee}`];
      delete appState.checkedTree[`company:${check.dataset.company}`];
    }
  }
}

function setCompanyChecked(company, checked) {
  const key = `company:${company.id}`;
  if (checked) {
    appState.checkedTree[key] = { scope: "company", id: company.id, name: company.name };
  } else {
    delete appState.checkedTree[key];
  }
  for (const emp of company.employees || []) {
    setEmployeeChecked(company.id, emp, checked);
  }
}

function setEmployeeChecked(companyId, emp, checked) {
  const key = `employee:${emp.id}`;
  if (checked) {
    appState.checkedTree[key] = { scope: "employee", id: emp.id, companyId, name: emp.name };
  } else {
    delete appState.checkedTree[key];
  }
  for (const [index, name] of (emp.talents || []).entries()) {
    setAccountChecked({ companyId, employeeId: emp.id, index, user: name, name }, checked);
  }
}

function setAccountChecked(item, checked) {
  const key = `talent:${accountKey(item.companyId, item.employeeId, item.user, item.index)}`;
  if (checked) {
    appState.checkedTree[key] = { scope: "talent", id: accountKey(item.companyId, item.employeeId, item.user, item.index), ...item };
  } else {
    delete appState.checkedTree[key];
  }
}

function namesForCheckedItem(item) {
  if (item.scope === "talent") return item.user ? [item.user] : [];
  if (item.scope === "employee") {
    const emp = findEmployee(item.id);
    return emp ? (emp.talents || []) : [];
  }
  if (item.scope === "company") {
    const company = appState.companies.find(c => c.id === item.id);
    if (!company) return [];
    return (company.employees || []).flatMap(emp => emp.talents || []);
  }
  return [];
}

function findCompany(companyId) {
  return appState.companies.find(c => c.id === companyId) || null;
}

function findEmployee(employeeId) {
  for (const company of appState.companies) {
    for (const emp of company.employees || []) {
      if (emp.id === employeeId) return emp;
    }
  }
  return null;
}

function findEmployeeInCompany(companyId, employeeId) {
  const company = findCompany(companyId);
  if (!company) return null;
  return (company.employees || []).find(emp => emp.id === employeeId) || null;
}

function companyAccounts(company) {
  return uniqueNames((company.employees || []).flatMap(emp => emp.talents || []));
}

function uniqueNames(names) {
  const seen = new Set();
  const result = [];
  for (const item of names || []) {
    const name = String(item || "").trim();
    if (!name) continue;
    const key = name.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(name);
  }
  return result;
}

function money(value) {
  const text = Number(value || 0).toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const symbol = currencySymbol(currentCountry());
  return symbol ? `${symbol} ${text}` : text;
}

function currentCountry() {
  return filterCountry();
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

document.addEventListener("click", async (event) => {
  if (!event.target.closest("#treeMenu")) hideTreeMenu();
  const check = event.target.closest(".tree-check");
  if (check) {
    toggleTreeCheck(check);
    appState.selected = { scope: "all", id: "", usernames: [] };
    clearStats();
    renderTree();
    return;
  }
  const addEmployeeBtn = event.target.closest(".add-employee-btn");
  if (addEmployeeBtn) {
    addEmployee(addEmployeeBtn.dataset.company);
    return;
  }
  const copyCompanyBtn = event.target.closest(".copy-company-btn");
  if (copyCompanyBtn) {
    await copyCompanyAccounts(copyCompanyBtn.dataset.company).catch(err => toast(err.message));
    return;
  }
  const deleteCompanyBtn = event.target.closest(".delete-company-btn");
  if (deleteCompanyBtn) {
    if (hasCheckedItems()) {
      await deleteCheckedItems().catch(err => toast(err.message));
      return;
    }
    await deleteCompanyDirect(deleteCompanyBtn.dataset.company).catch(err => toast(err.message));
    return;
  }
  const addAccountBtn = event.target.closest(".add-account-btn");
  if (addAccountBtn) {
    addAccount(addAccountBtn.dataset.company, addAccountBtn.dataset.employee);
    return;
  }
  const copyEmployeeBtn = event.target.closest(".copy-employee-btn");
  if (copyEmployeeBtn) {
    await copyEmployeeAccounts(copyEmployeeBtn.dataset.company, copyEmployeeBtn.dataset.employee).catch(err => toast(err.message));
    return;
  }
  const deleteEmployeeBtn = event.target.closest(".delete-employee-btn");
  if (deleteEmployeeBtn) {
    if (hasCheckedItems()) {
      await deleteCheckedItems().catch(err => toast(err.message));
      return;
    }
    await deleteEmployeeDirect(deleteEmployeeBtn.dataset.company, deleteEmployeeBtn.dataset.employee).catch(err => toast(err.message));
    return;
  }
  const node = event.target.closest(".tree-node");
  if (node) {
    const changedCheck = selectTreeNode(node);
    if (changedCheck) {
      appState.selected = { scope: "all", id: "", usernames: [] };
      clearStats();
    }
    renderTree();
  }
});

document.addEventListener("contextmenu", (event) => {
  const node = event.target.closest(".tree-node");
  if (!node) {
    hideTreeMenu();
    return;
  }
  showTreeMenu(event, node);
});

$("importBtn").addEventListener("click", () => importBill().catch(err => toast(err.message)));
$("selectBillFileBtn").addEventListener("click", () => chooseBillFile().catch(err => toast(err.message)));
$("billFile").addEventListener("change", () => {
  const file = $("billFile").files[0];
  setSelectedBillFile("", file ? file.name : "");
});
$("countrySelect").addEventListener("change", () => {
  if ($("filterCountry") && !$("filterCountry").value) {
    $("filterCountry").value = $("countrySelect").value;
  }
});
$("filterCountry").addEventListener("change", () => {
  applyBillDateRange();
  clearStats();
});
$("settlementStatus").addEventListener("change", () => {
  clearStats();
});
["usdRate", "rmbRate", "agencyCutRate", "feePermille"].forEach(id => {
  const input = $(id);
  if (input) input.addEventListener("input", updateAgencySettlement);
});
$("ordersPageBtn").addEventListener("click", (event) => {
  event.preventDefault();
  saveStatsReturnState();
  const query = appState.lastQuery || currentParams().toString();
  window.location.href = `/orders?${query}`;
});
$("manualSearchBtn").addEventListener("click", () => {
  const usernames = splitAccountLines($("manualUsers").value);
  $("manualUsers").value = usernames.join("\n");
  appState.selected = usernames.length
    ? { scope: "manual", id: "", usernames }
    : { scope: "all", id: "", usernames: [] };
  renderTree();
  runStats().catch(err => toast(err.message));
});
$("manualClearBtn").addEventListener("click", () => {
  $("manualUsers").value = "";
  appState.selected = { scope: "all", id: "", usernames: [] };
  renderTree();
  clearStats();
});
$("addCompanyBtn").addEventListener("click", addCompany);
$("statCheckedBtn").addEventListener("click", openStatModal);
$("deleteCheckedBtn").addEventListener("click", () => deleteCheckedItems().catch(err => toast(err.message)));
$("confirmInputModalBtn").addEventListener("click", () => confirmInputModal().catch(err => toast(err.message)));
$("cancelInputModalBtn").addEventListener("click", closeInputModal);
$("closeInputModalBtn").addEventListener("click", closeInputModal);
$("inputModal").addEventListener("click", (event) => {
  if (event.target.id === "inputModal") closeInputModal();
});
$("confirmStatModalBtn").addEventListener("click", () => confirmStatModal().catch(err => toast(err.message)));
$("cancelStatModalBtn").addEventListener("click", closeStatModal);
$("closeStatModalBtn").addEventListener("click", closeStatModal);
$("statModal").addEventListener("click", (event) => {
  if (event.target.id === "statModal") closeStatModal();
});
$("closeConfirmModalBtn").addEventListener("click", () => closeConfirmModal(false));
$("cancelConfirmModalBtn").addEventListener("click", () => closeConfirmModal(false));
$("confirmConfirmModalBtn").addEventListener("click", () => closeConfirmModal(true));
$("confirmModal").addEventListener("click", (event) => {
  if (event.target.id === "confirmModal") closeConfirmModal(false);
});
$("editTreeItemBtn").addEventListener("click", () => editTreeItem(appState.menuTarget).catch(err => toast(err.message)));
$("deleteTreeItemBtn").addEventListener("click", () => deleteTreeItem(appState.menuTarget).catch(err => toast(err.message)));
$("exportSummaryBtn").addEventListener("click", () => exportCurrent("summary"));
$("exportOrdersBtn").addEventListener("click", () => exportCurrent("orders"));
$("exportBothBtn").addEventListener("click", () => exportCurrent("both"));
$("summaryBody").addEventListener("click", (event) => {
  const btn = event.target.closest(".summary-account");
  if (!btn) return;
  openTalentOrders(btn.dataset.user);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    if ($("confirmModal").classList.contains("show")) {
      closeConfirmModal(false);
      return;
    }
    closeInputModal();
    closeStatModal();
    hideTreeMenu();
  }
});

bindDatePickers();
bindAutoScale();
loadState().catch(err => toast(err.message));


let latestUpdateDownloadURL = "";

document.addEventListener("DOMContentLoaded", () => {
  const checkBtn = $("checkUpdateBtn");
  if (checkBtn) checkBtn.addEventListener("click", openUpdateModal);
  if ($("closeUpdateModalBtn")) $("closeUpdateModalBtn").addEventListener("click", closeUpdateModal);
  if ($("cancelUpdateModalBtn")) $("cancelUpdateModalBtn").addEventListener("click", closeUpdateModal);
  if ($("saveUpdateSourceBtn")) $("saveUpdateSourceBtn").addEventListener("click", saveAndCheckUpdate);
  if ($("applyUpdateBtn")) $("applyUpdateBtn").addEventListener("click", applySoftwareUpdate);
});

async function openUpdateModal() {
  const modal = $("updateModal");
  if (!modal) return;
  modal.classList.add("show");
  modal.setAttribute("aria-hidden", "false");
  await runUpdateCheck();
}

function closeUpdateModal() {
  const modal = $("updateModal");
  if (!modal) return;
  modal.classList.remove("show");
  modal.setAttribute("aria-hidden", "true");
}

async function saveAndCheckUpdate() {
  const raw = ($("updateSourceInput")?.value || "").trim();
  const payload = raw.startsWith("http://") || raw.startsWith("https://")
    ? (raw.includes("github.com/") && !raw.endsWith(".json") ? { repo: raw, url: "" } : { repo: "", url: raw })
    : { repo: raw, url: "" };
  try {
    await api("/api/update/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    await runUpdateCheck();
  } catch (err) {
    if ($("updateStatusText")) $("updateStatusText").textContent = "保存失败：" + err.message;
  }
}

async function runUpdateCheck() {
  const statusEl = $("updateStatusText");
  const applyBtn = $("applyUpdateBtn");
  latestUpdateDownloadURL = "";
  if (applyBtn) applyBtn.disabled = true;
  if (statusEl) statusEl.textContent = "正在检查更新，请稍候...";
  try {
    const data = await api("/api/update/check");
    if ($("updateCurrentVer")) $("updateCurrentVer").textContent = "v" + (data.currentVersion || "-");
    if ($("updateLatestVer")) $("updateLatestVer").textContent = data.latestVersion ? "v" + data.latestVersion : "-";
    if ($("updateSourceInput") && !$("updateSourceInput").value) {
      $("updateSourceInput").value = data.url || data.repo || "";
    }
    if (!data.configured) {
      if (statusEl) statusEl.textContent = "尚未配置更新源，请在下方填入 GitHub 仓库（如 owner/repo）或 version.json 地址后点击「保存并检查」。";
      return;
    }
    if (data.hasUpdate) {
      latestUpdateDownloadURL = data.downloadUrl || "";
      if (applyBtn) applyBtn.disabled = !latestUpdateDownloadURL;
      if (statusEl) statusEl.textContent = `发现新版本 v${data.latestVersion}！\n${data.releaseNotes || "点击下方「立即更新并重启」将自动下载覆盖并重启。"}`;
    } else {
      if (statusEl) statusEl.textContent = "当前已是最新版本。";
    }
  } catch (err) {
    if (statusEl) statusEl.textContent = "检查更新失败：" + err.message;
  }
}

async function applySoftwareUpdate() {
  if (!latestUpdateDownloadURL) return;
  const statusEl = $("updateStatusText");
  const applyBtn = $("applyUpdateBtn");
  if (applyBtn) {
    applyBtn.disabled = true;
    applyBtn.textContent = "正在下载更新...";
  }
  if (statusEl) statusEl.textContent = "正在下载新版程序并准备覆盖旧版，完成后将自动重启...";
  try {
    const res = await api("/api/update/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ downloadUrl: latestUpdateDownloadURL })
    });
    if (statusEl) statusEl.textContent = res.message || "更新已下载，正在覆盖并重启...";
  } catch (err) {
    if (statusEl) statusEl.textContent = "更新失败：" + err.message;
    if (applyBtn) {
      applyBtn.disabled = false;
      applyBtn.textContent = "立即更新并重启";
    }
  }
}
