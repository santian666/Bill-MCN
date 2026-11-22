const appState = {
  countries: [],
  settlementStatuses: [],
  companies: [],
  dateRange: null,
  countryDateRanges: {},
  selected: { scope: "all", id: "", usernames: [] },
  checkedTree: {},
  collapsedCompanies: {},
  collapsedEmployees: {},
  menuTarget: null,
  inputHandler: null,
  lastQuery: "",
  hasStats: false
};

const $ = (id) => document.getElementById(id);

function bindAutoScale() {
  const update = () => {
    const scale = Math.max(0.72, Math.min(1, window.innerWidth / 1366, window.innerHeight / 768));
    document.documentElement.style.setProperty("--ui-scale", scale.toFixed(3));
  };
  update();
  window.addEventListener("resize", update);
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
  const data = await api("/api/state");
  appState.countries = data.countries || [];
  appState.settlementStatuses = data.settlementStatuses || [];
  appState.companies = data.companies || [];
  appState.dateRange = data.dateRange || null;
  appState.countryDateRanges = data.countryDateRanges || {};
  pruneCheckedTree();
  renderCountries(selectedCountry);
  renderSettlementStatuses();
  applyBillDateRange();
  clearStats();
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

function renderCountries(selectedCountry = "") {
  $("countrySelect").innerHTML = appState.countries.map(c => `<option value="${escapeHtml(c)}">${escapeHtml(c)}</option>`).join("");
  if (selectedCountry && appState.countries.includes(selectedCountry)) {
    $("countrySelect").value = selectedCountry;
  }
}

function renderSettlementStatuses() {
  const html = `<option value="">全部状态</option>` + appState.settlementStatuses.map(s => `<option value="${escapeHtml(s)}">${escapeHtml(s)}</option>`).join("");
  $("settlementStatus").innerHTML = html;
  if ($("statSettlementStatus")) $("statSettlementStatus").innerHTML = html;
}

function currentDateRange() {
  const country = $("countrySelect").value;
  return appState.countryDateRanges[country] || appState.dateRange || {};
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
      for (const name of emp.talents || []) {
        valid[`talent:${accountKey(company.id, emp.id, name)}`] = true;
      }
    }
  }
  for (const key of Object.keys(appState.checkedTree)) {
    if (!valid[key]) delete appState.checkedTree[key];
  }
}

function renderCompany(company) {
  const collapsed = !!appState.collapsedCompanies[company.id];
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
  const collapsed = !!appState.collapsedEmployees[emp.id];
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
      ${accounts.length ? accounts.map(name => renderAccount(companyId, emp.id, name)).join("") : `<div class="empty">暂无账号</div>`}
    </div>
  `;
}

function renderAccount(companyId, employeeId, name) {
  return `
    <div class="tree-line account-line">
      <input class="tree-check" type="checkbox" ${isChecked("talent", accountKey(companyId, employeeId, name)) ? "checked" : ""} data-scope="talent" data-id="${escapeHtml(accountKey(companyId, employeeId, name))}" data-company="${companyId}" data-employee="${employeeId}" data-user="${escapeHtml(name)}" data-name="${escapeHtml(name)}" title="勾选账号">
      <div class="tree-node account-node ${isActive("talent", name)}" data-scope="talent" data-id="${escapeHtml(name)}" data-company="${companyId}" data-employee="${employeeId}" data-user="${escapeHtml(name)}" data-name="${escapeHtml(name)}">${escapeHtml(name)}</div>
    </div>
  `;
}

function isActive(scope, id) {
  return appState.selected.scope === scope && appState.selected.id === id ? "active" : "";
}

function isChecked(scope, id) {
  return !!appState.checkedTree[`${scope}:${id}`];
}

function accountKey(companyId, employeeId, name) {
  return `${companyId}|${employeeId}|${name}`;
}

function currentParams() {
  const p = new URLSearchParams();
  const country = $("countrySelect").value;
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
  $("resultTitle").textContent = "筛选结果";
  $("resultHint").textContent = "";
  $("totalIncome").textContent = "";
  $("orderCount").textContent = "";
  $("talentCount").textContent = "";
  $("exportOrderCount").textContent = "";
  $("exportIncome").textContent = "";
  $("statusStats").innerHTML = "";
  $("summaryBody").innerHTML = `<tr><td colspan="7"></td></tr>`;
  setExportButtonsDisabled(true);
}

async function runStats() {
  if (!$("countrySelect").value) {
    clearStats();
    toast("请先选择国家");
    return;
  }
  const params = currentParams();
  appState.lastQuery = params.toString();
  setStatus("正在统计...");
  const data = await api(`/api/stats?${appState.lastQuery}`);
  appState.hasStats = true;
  renderStats(data);
  setStatus("统计完成");
}

function renderStats(data) {
  $("resultTitle").textContent = "筛选结果";
  $("resultHint").textContent = filterText(data);
  $("totalIncome").textContent = money(statusTotal(data.statusStats, "已结算"));
  $("orderCount").textContent = data.orderCount || 0;
  $("talentCount").textContent = data.talentCount || 0;
  $("exportOrderCount").textContent = data.orderCount || 0;
  $("exportIncome").textContent = money(data.total || 0);
  setExportButtonsDisabled(false);
  renderStatusStats(data.statusStats || []);
  const perTalent = data.perTalent || [];
  $("summaryBody").innerHTML = perTalent.length ? perTalent.map(item => `
    <tr data-user="${escapeHtml(item.username)}" class="summary-row">
      <td>${escapeHtml(item.username)}</td>
      <td>${money(talentStatusTotal(item, "已结算"))}</td>
      <td>${money(talentStatusTotal(item, "客户未付款"))}</td>
      <td>${money(talentStatusTotal(item, "待确认"))}</td>
      <td>${money(talentStatusTotal(item, "不符合条件"))}</td>
      <td>${money(talentStatusTotal(item, "已冻结"))}</td>
      <td>${item.orders}</td>
    </tr>
  `).join("") : `<tr><td colspan="7">暂无数据</td></tr>`;
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
  window.location.href = `/api/export?${query.toString()}`;
}

function filterText(data) {
  const scope = data.title || "全部订单";
  const country = $("countrySelect").value || "全部国家";
  const start = $("startDate").value || "最早";
  const end = $("endDate").value || "最晚";
  const status = $("settlementStatus").value || "全部状态";
  return `已选择的筛选条件：${scope}，国家：${country}，时间：${start} 至 ${end}，订单结算状态：${status}`;
}

function renderStatusStats(items) {
  const stats = statusStatsMap(items);
  const statuses = appState.settlementStatuses.length ? appState.settlementStatuses : ["客户未付款", "待确认", "已结算", "不符合条件", "已冻结"];
  $("statusStats").innerHTML = statuses.map(status => {
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

function talentStatusTotal(item, status) {
  return item && item.statusTotals ? item.statusTotals[status] || 0 : 0;
}

async function importBill() {
  const file = $("billFile").files[0];
  if (!file) {
    toast("请选择账单文件");
    return;
  }
  $("importBtn").disabled = true;
  $("importBtn").textContent = "导入中...";
  try {
    let data = await uploadBill(file, false);
    if (data.requiresConfirm) {
      const confirmed = window.confirm(importConfirmText(data));
      if (!confirmed) {
        toast("已取消导入更新");
        return;
      }
      $("importBtn").textContent = "更新中...";
      data = await uploadBill(file, true);
    }
    toast(data.message || "导入完成");
    await loadState();
  } finally {
    $("importBtn").disabled = false;
    $("importBtn").textContent = "导入账单";
  }
}

function uploadBill(file, confirmUpdate) {
  const fd = new FormData();
  fd.append("country", $("countrySelect").value);
  fd.append("file", file);
  if (confirmUpdate) fd.append("confirmUpdate", "1");
  return api("/api/import", { method: "POST", body: fd });
}

function importConfirmText(data) {
  const lines = [
    `发现 ${data.duplicateCount || 0} 条重复订单。`,
    `其中 ${data.changedDuplicateCount || 0} 条有字段变化，${data.unchangedDuplicateCount || 0} 条内容相同。`,
    "是否用新导入的数据覆盖旧数据？"
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
  applyStatDateRange();
  $("statModal").classList.add("show");
  $("statModal").setAttribute("aria-hidden", "false");
}

function closeStatModal() {
  $("statModal").classList.remove("show");
  $("statModal").setAttribute("aria-hidden", "true");
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
  $("confirmInputModalBtn").disabled = true;
  try {
    await handler(text);
    closeInputModal();
  } finally {
    $("confirmInputModalBtn").disabled = false;
  }
}

function addCompany() {
  openInputModal("新增公司", "一行一个公司名称", "", async (text) => {
    const names = splitInputLines(text);
    let lastCompany = null;
    for (const name of names) {
      lastCompany = await api("/api/company", { method: "POST", headers: jsonHeader(), body: JSON.stringify({ name }) });
    }
    if (lastCompany) appState.selected = { scope: "company", id: lastCompany.id, usernames: [] };
    await loadState();
    toast("公司已新增");
  });
}

function addEmployee(companyId) {
  if (!companyId) return toast("请先选择公司");
  openInputModal("新增员工", "一行一个员工名称", "", async (text) => {
    const data = await api("/api/employee", { method: "POST", headers: jsonHeader(), body: JSON.stringify({ companyId, name: text }) });
    const employees = data.employees || [];
    if (employees.length) {
      const last = employees[employees.length - 1];
      appState.selected = { scope: "employee", id: last.id, usernames: [] };
      appState.collapsedCompanies[companyId] = false;
    }
    await loadState();
    toast("员工已新增");
  });
}

function addAccount(companyId, employeeId) {
  if (!companyId || !employeeId) return toast("请先选择员工");
  openInputModal("新增账号", "一行一个达人账号", "", async (text) => {
    await api("/api/talent", { method: "POST", headers: jsonHeader(), body: JSON.stringify({ companyId, employeeId, usernames: text }) });
    appState.selected = { scope: "employee", id: employeeId, usernames: [] };
    appState.collapsedCompanies[companyId] = false;
    appState.collapsedEmployees[employeeId] = false;
    await loadState();
    toast("账号已新增");
  });
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
      const newName = firstLine(text);
      await api("/api/talent", { method: "PUT", headers: jsonHeader(), body: JSON.stringify({ companyId: target.companyId, employeeId: target.employeeId, oldName: target.name, newName }) });
      appState.selected = { scope: "talent", id: newName, usernames: [newName] };
      await loadState();
      toast("账号已修改");
    });
  }
}

async function deleteTreeItem(target) {
  hideTreeMenu();
  if (!target) return;
  const label = target.scope === "company" ? "公司" : target.scope === "employee" ? "员工" : "账号";
  if (!window.confirm(`确定删除${label}：${target.name}？`)) return;
  if (target.scope === "company") {
    await api("/api/company", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ id: target.id }) });
  } else if (target.scope === "employee") {
    await api("/api/employee", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ companyId: target.companyId, employeeId: target.id }) });
  } else if (target.scope === "talent") {
    await api("/api/talent", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ companyId: target.companyId, employeeId: target.employeeId, name: target.name }) });
  }
  appState.selected = { scope: "all", id: "", usernames: [] };
  await loadState();
  toast(`${label}已删除`);
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
  if (!window.confirm(`确定删除公司：${company.name}？\n将同时删除 ${count} 个账号记录。`)) return;
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
  if (!window.confirm(`确定删除员工：${employee.name}？\n将同时删除 ${count} 个账号记录。`)) return;
  await api("/api/employee", { method: "DELETE", headers: jsonHeader(), body: JSON.stringify({ companyId, employeeId }) });
  appState.selected = { scope: "all", id: "", usernames: [] };
  clearStats();
  await loadState();
  toast("员工已删除");
}

function selectTreeNode(node) {
  const scope = node.dataset.scope;
  if (scope === "company") {
    appState.collapsedCompanies[node.dataset.id] = !appState.collapsedCompanies[node.dataset.id];
  } else if (scope === "employee") {
    appState.collapsedEmployees[node.dataset.id] = !appState.collapsedEmployees[node.dataset.id];
  }
}

function showTreeMenu(event, node) {
  event.preventDefault();
  appState.menuTarget = {
    scope: node.dataset.scope,
    id: node.dataset.id,
    name: node.dataset.name || node.dataset.user || "",
    companyId: node.dataset.company || "",
    employeeId: node.dataset.employee || ""
  };
  const menu = $("treeMenu");
  menu.style.left = `${Math.min(event.clientX, window.innerWidth - 120)}px`;
  menu.style.top = `${Math.min(event.clientY, window.innerHeight - 86)}px`;
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

function firstLine(text) {
  return splitInputLines(text)[0] || "";
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
    const company = appState.companies.find(c => c.id === check.dataset.id);
    if (company) setCompanyChecked(company, checked);
  } else if (scope === "employee") {
    const employee = findEmployee(check.dataset.id);
    if (employee) setEmployeeChecked(check.dataset.company, employee, checked);
    if (!checked) delete appState.checkedTree[`company:${check.dataset.company}`];
  } else if (scope === "talent") {
    setAccountChecked({
      companyId: check.dataset.company || "",
      employeeId: check.dataset.employee || "",
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
    appState.collapsedCompanies[company.id] = false;
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
    appState.collapsedEmployees[emp.id] = false;
  } else {
    delete appState.checkedTree[key];
  }
  for (const name of emp.talents || []) {
    setAccountChecked({ companyId, employeeId: emp.id, user: name, name }, checked);
  }
}

function setAccountChecked(item, checked) {
  const key = `talent:${accountKey(item.companyId, item.employeeId, item.user)}`;
  if (checked) {
    appState.checkedTree[key] = { scope: "talent", id: accountKey(item.companyId, item.employeeId, item.user), ...item };
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
  const el = $("countrySelect");
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
    await deleteEmployeeDirect(deleteEmployeeBtn.dataset.company, deleteEmployeeBtn.dataset.employee).catch(err => toast(err.message));
    return;
  }
  const node = event.target.closest(".tree-node");
  if (node) {
    selectTreeNode(node);
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
$("countrySelect").addEventListener("change", () => {
  applyBillDateRange();
  clearStats();
});
$("settlementStatus").addEventListener("change", () => {
  clearStats();
});
$("ordersPageBtn").addEventListener("click", (event) => {
  event.preventDefault();
  const query = appState.lastQuery || currentParams().toString();
  window.location.href = `/orders?${query}`;
});
$("manualSearchBtn").addEventListener("click", () => {
  const usernames = splitInputLines($("manualUsers").value);
  appState.selected = usernames.length
    ? { scope: "manual", id: "", usernames }
    : { scope: "all", id: "", usernames: [] };
  renderTree();
  runStats().catch(err => toast(err.message));
});
$("addCompanyBtn").addEventListener("click", addCompany);
$("statCheckedBtn").addEventListener("click", openStatModal);
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
$("editTreeItemBtn").addEventListener("click", () => editTreeItem(appState.menuTarget).catch(err => toast(err.message)));
$("deleteTreeItemBtn").addEventListener("click", () => deleteTreeItem(appState.menuTarget).catch(err => toast(err.message)));
$("exportSummaryBtn").addEventListener("click", () => exportCurrent("summary"));
$("exportOrdersBtn").addEventListener("click", () => exportCurrent("orders"));
$("exportBothBtn").addEventListener("click", () => exportCurrent("both"));
$("summaryBody").addEventListener("click", async (event) => {
  const row = event.target.closest(".summary-row");
  if (!row) return;
  appState.selected = { scope: "talent", id: row.dataset.user, usernames: [row.dataset.user] };
  renderTree();
  await runStats().catch(err => toast(err.message));
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    closeInputModal();
    closeStatModal();
    hideTreeMenu();
  }
});

bindDatePickers();
bindAutoScale();
loadState().catch(err => toast(err.message));
