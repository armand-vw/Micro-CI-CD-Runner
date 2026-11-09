/* Micro-CI/CD Runner dashboard. Vanilla JS, no build step. */
(() => {
  "use strict";

  const config = window.RUNNER_CONFIG || {};
  const API_BASE = String(config.apiBase || "").replace(/\/+$/, "");
  const POLL_MS = 20000;

  const state = {
    projects: [],
    builds: [],
    stats: { total: 0, pending: 0, running: 0, success: 0, failed: 0 },
    tab: "overview",
    filters: { projectId: "", status: "", q: "" },
    selected: null,
    selectedLogs: "",
    logLoading: false,
    pendingChunks: [],
    autoScroll: true,
    eventSource: null,
  };

  const $ = (id) => document.getElementById(id);
  const els = {
    projectList: $("project-list"),
    pageTitle: $("page-title"),
    pageSubtitle: $("page-subtitle"),
    connection: $("connection"),
    connectionText: $("connection-text"),
    statTotal: $("stat-total"),
    statRunning: $("stat-running"),
    statSuccess: $("stat-success"),
    statFailed: $("stat-failed"),
    recentBuilds: $("recent-builds"),
    recentEmpty: $("recent-empty"),
    buildsTable: $("builds-table"),
    buildsEmpty: $("builds-empty"),
    filterProject: $("filter-project"),
    filterStatus: $("filter-status"),
    filterSearch: $("filter-search"),
    projectCards: $("project-cards"),
    projectsEmpty: $("projects-empty"),
    drawer: $("drawer"),
    drawerTitle: $("drawer-title"),
    drawerMeta: $("drawer-meta"),
    drawerFacts: $("drawer-facts"),
    drawerClose: $("drawer-close"),
    scrim: $("scrim"),
    logView: $("log-view"),
    logLive: $("log-live"),
    autoscroll: $("autoscroll"),
    downloadLogs: $("download-logs"),
    retryBuild: $("retry-build"),
    refresh: $("refresh"),
    themeToggle: $("theme-toggle"),
    toast: $("toast"),
  };

  let logFrame = null;
  let toastTimer = null;

  /* ------------------------------------------------------------- utilities */

  function api(path) {
    return API_BASE + path;
  }

  async function fetchJSON(path) {
    const res = await fetch(api(path), { headers: { Accept: "application/json" } });
    if (!res.ok) throw new Error(await responseError(res));
    return res.json();
  }

  async function responseError(res) {
    try {
      const data = await res.json();
      return data.error || res.statusText;
    } catch (_) {
      return res.statusText || `HTTP ${res.status}`;
    }
  }

  function esc(value) {
    return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    })[ch]);
  }

  function shortSHA(sha) {
    return sha ? String(sha).slice(0, 7) : "—";
  }

  function timeAgo(iso) {
    if (!iso) return "—";
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return "—";
    const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
    if (seconds < 45) return "just now";
    if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`;
    if (seconds < 604800) return `${Math.round(seconds / 86400)}d ago`;
    return new Date(iso).toLocaleDateString();
  }

  function duration(build) {
    if (!build.started_at) return "—";
    const start = new Date(build.started_at).getTime();
    if (Number.isNaN(start)) return "—";
    const end = build.finished_at ? new Date(build.finished_at).getTime() : Date.now();
    const seconds = Math.max(0, Math.round((end - start) / 1000));
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    const rest = seconds % 60;
    if (minutes < 60) return `${minutes}m ${rest}s`;
    return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
  }

  function statusPill(status) {
    return `<span class="pill ${esc(status)}">${esc(status)}</span>`;
  }

  function projectName(id) {
    const project = state.projects.find((p) => p.id === id);
    return project ? project.name : `Project ${id}`;
  }

  function branchName(ref) {
    return String(ref || "").replace("refs/heads/", "") || "—";
  }

  function latestByProject() {
    const latest = new Map();
    for (const build of state.builds) {
      if (!latest.has(build.project_id)) latest.set(build.project_id, build);
    }
    return latest;
  }

  function toast(message) {
    els.toast.textContent = message;
    els.toast.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => els.toast.classList.remove("show"), 4000);
  }

  /* --------------------------------------------------------------- rendering */

  function renderStats() {
    els.statTotal.textContent = state.stats.total ?? 0;
    els.statRunning.textContent = state.stats.running ?? 0;
    els.statSuccess.textContent = state.stats.success ?? 0;
    els.statFailed.textContent = state.stats.failed ?? 0;
  }

  function renderProjectList() {
    if (!state.projects.length) {
      els.projectList.innerHTML = '<li class="list-empty">No projects</li>';
      return;
    }
    const latest = latestByProject();
    els.projectList.innerHTML = state.projects.map((project) => {
      const build = latest.get(project.id);
      return `<li>
        <button type="button" data-project="${project.id}">
          <span class="name">${esc(project.name)}</span>
          <span class="status-dot ${build ? esc(build.status) : ""}" title="${build ? esc(build.status) : "no builds"}"></span>
        </button>
      </li>`;
    }).join("");
  }

  function renderRecent() {
    const rows = state.builds.slice(0, 8);
    els.recentBuilds.innerHTML = rows.map((build) => `
      <tr data-build="${build.id}">
        <td class="cell-strong">#${build.id}</td>
        <td>${esc(build.project_name || projectName(build.project_id))}</td>
        <td class="cell-mono">${esc(shortSHA(build.commit_sha))}</td>
        <td>${statusPill(build.status)}</td>
        <td class="cell-muted">${esc(duration(build))}</td>
        <td class="cell-muted">${esc(timeAgo(build.created_at))}</td>
      </tr>`).join("");
    els.recentEmpty.classList.toggle("hidden", rows.length > 0);
  }

  function filteredBuilds() {
    const { projectId, status, q } = state.filters;
    const needle = q.trim().toLowerCase();
    return state.builds.filter((build) => {
      if (projectId && String(build.project_id) !== projectId) return false;
      if (status && build.status !== status) return false;
      if (needle) {
        const haystack = `${build.id} ${build.project_name || ""} ${build.commit_sha || ""} ${build.ref || ""}`.toLowerCase();
        if (!haystack.includes(needle)) return false;
      }
      return true;
    });
  }

  function renderBuildsTable() {
    const rows = filteredBuilds();
    els.buildsTable.innerHTML = rows.map((build) => `
      <tr data-build="${build.id}">
        <td class="cell-strong">#${build.id}</td>
        <td>${esc(build.project_name || projectName(build.project_id))}</td>
        <td class="cell-mono">${esc(branchName(build.ref))}</td>
        <td class="cell-mono">${esc(shortSHA(build.commit_sha))}</td>
        <td>${statusPill(build.status)}</td>
        <td class="cell-muted">${esc(timeAgo(build.created_at))}</td>
      </tr>`).join("");
    els.buildsEmpty.classList.toggle("hidden", rows.length > 0);
  }

  function renderFilterOptions() {
    const current = els.filterProject.value;
    const options = ['<option value="">All projects</option>'].concat(
      state.projects.map((project) => `<option value="${project.id}">${esc(project.name)}</option>`)
    );
    els.filterProject.innerHTML = options.join("");
    if (Array.from(els.filterProject.options).some((option) => option.value === current)) {
      els.filterProject.value = current;
    }
  }

  function renderProjectCards() {
    els.projectsEmpty.classList.toggle("hidden", state.projects.length > 0);
    els.projectCards.innerHTML = state.projects.map((project) => {
      const builds = state.builds.filter((build) => build.project_id === project.id);
      const last = builds[0];
      const repo = /^https?:\/\//i.test(project.repo_url) ? project.repo_url : `https://${project.repo_url}`;
      return `<article class="project-card">
        <div>
          <h3>${esc(project.name)}</h3>
          <a class="repo" href="${esc(repo)}" target="_blank" rel="noopener noreferrer">${esc(repo)}</a>
        </div>
        <dl>
          <dt>Branch</dt><dd class="cell-mono">${esc(project.branch)}</dd>
          <dt>Image</dt><dd class="cell-mono">${esc(project.image)}</dd>
          <dt>Test</dt><dd class="cell-mono">${esc(project.test_command)}</dd>
          <dt>Builds</dt><dd>${builds.length}</dd>
        </dl>
        <div class="card-actions">
          <span>${last ? statusPill(last.status) : '<span class="cell-muted">No builds</span>'}</span>
          <button class="text-btn" type="button" data-project="${project.id}">View builds</button>
        </div>
      </article>`;
    }).join("");
  }

  function renderAll() {
    renderStats();
    renderProjectList();
    renderRecent();
    renderBuildsTable();
    renderFilterOptions();
    renderProjectCards();
  }

  /* ---------------------------------------------------------------- drawer */

  function renderDrawer() {
    const build = state.selected;
    if (!build) return;

    els.drawerTitle.textContent = `Build #${build.id}`;
    els.drawerMeta.textContent = `${build.project_name || projectName(build.project_id)} · ${branchName(build.ref)}`;

    const facts = [
      ["Status", statusPill(build.status)],
      ["Commit", `<span class="cell-mono">${esc(build.commit_sha || "—")}</span>`],
      ["Branch", esc(branchName(build.ref))],
      ["Created", esc(timeAgo(build.created_at))],
      ["Started", esc(build.started_at ? timeAgo(build.started_at) : "—")],
      ["Duration", esc(duration(build))],
    ];
    els.drawerFacts.innerHTML = facts
      .map(([label, value]) => `<div><dt>${label}</dt><dd>${value}</dd></div>`)
      .join("");

    els.downloadLogs.href = api(`/api/builds/${build.id}/logs?download=1`);
    els.retryBuild.disabled = build.status === "pending" || build.status === "running";
    els.logLive.className = `log-live${build.status === "running" ? " running" : ""}`;
    els.logLive.textContent = build.status === "running" ? "live logs" : "logs";
  }

  function renderLog(forceScroll = false) {
    const hasLogs = state.selectedLogs.length > 0;
    const waiting = state.selected && state.selected.status === "running";
    els.logView.textContent = hasLogs ? state.selectedLogs : (waiting ? "Waiting for output…" : "No output captured.");
    if (state.autoScroll || forceScroll) {
      els.logView.scrollTop = els.logView.scrollHeight;
    }
  }

  function scheduleLogRender() {
    if (logFrame) return;
    logFrame = requestAnimationFrame(() => {
      logFrame = null;
      renderLog();
    });
  }

  async function openDrawer(build) {
    state.selected = build;
    state.selectedLogs = "";
    state.pendingChunks = [];
    state.logLoading = true;
    state.autoScroll = true;
    els.autoscroll.checked = true;
    renderDrawer();
    renderLog(true);

    els.drawer.classList.add("open");
    els.scrim.classList.add("open");
    els.drawer.setAttribute("aria-hidden", "false");

    try {
      const [fresh, logsRes] = await Promise.all([
        fetchJSON(`/api/builds/${build.id}`),
        fetch(api(`/api/builds/${build.id}/logs`)),
      ]);
      if (!state.selected || state.selected.id !== build.id) return;
      const logs = logsRes.ok ? await logsRes.text() : "";
      state.selected = fresh;
      state.selectedLogs = logs + state.pendingChunks.join("");
      state.pendingChunks = [];
      state.logLoading = false;
      renderDrawer();
      renderLog(true);
    } catch (err) {
      if (!state.selected || state.selected.id !== build.id) return;
      state.logLoading = false;
      toast(`Could not load build details: ${err.message}`);
    }
  }

  function closeDrawer() {
    state.selected = null;
    state.selectedLogs = "";
    state.pendingChunks = [];
    state.logLoading = false;
    els.drawer.classList.remove("open");
    els.scrim.classList.remove("open");
    els.drawer.setAttribute("aria-hidden", "true");
  }

  /* ------------------------------------------------------------- data flow */

  async function refreshAll(options = {}) {
    try {
      const [projects, builds, stats] = await Promise.all([
        fetchJSON("/api/projects"),
        fetchJSON("/api/builds?limit=100"),
        fetchJSON("/api/stats"),
      ]);
      state.projects = projects.projects || [];
      state.builds = builds.builds || [];
      state.stats = stats || state.stats;
      renderAll();
    } catch (err) {
      if (!options.silent) toast(`Refresh failed: ${err.message}`);
    }
  }

  async function refreshStats() {
    try {
      state.stats = await fetchJSON("/api/stats");
      renderStats();
    } catch (_) {
      /* non-critical */
    }
  }

  function setConnection(online) {
    els.connection.classList.toggle("online", online);
    els.connection.classList.toggle("offline", !online);
    els.connectionText.textContent = online ? "Live" : "Reconnecting";
  }

  function connectEvents() {
    if (state.eventSource) state.eventSource.close();
    const source = new EventSource(api("/api/events"));
    state.eventSource = source;

    source.onopen = () => setConnection(true);
    source.onerror = () => setConnection(false);

    source.addEventListener("build.created", (event) => handleBuildEvent(event, true));
    source.addEventListener("build.updated", (event) => handleBuildEvent(event, false));
    source.addEventListener("build.finished", (event) => {
      handleBuildEvent(event, false);
      refreshStats();
    });
    source.addEventListener("build.log", (event) => {
      let data;
      try {
        data = JSON.parse(event.data);
      } catch (_) {
        return;
      }
      if (!state.selected || Number(data.build_id) !== state.selected.id) return;
      if (state.logLoading) {
        state.pendingChunks.push(data.chunk);
      } else {
        state.selectedLogs += data.chunk;
        scheduleLogRender();
      }
    });
  }

  function handleBuildEvent(event, isCreated) {
    let build;
    try {
      build = JSON.parse(event.data);
    } catch (_) {
      return;
    }
    const index = state.builds.findIndex((item) => item.id === build.id);
    if (index >= 0) {
      state.builds[index] = { ...state.builds[index], ...build };
    } else {
      state.builds.unshift(build);
    }
    state.builds.sort((a, b) => b.id - a.id);

    if (state.selected && state.selected.id === build.id) {
      state.selected = { ...state.selected, ...build };
      renderDrawer();
    }

    renderRecent();
    renderBuildsTable();
    renderProjectList();
    renderProjectCards();
    if (isCreated) refreshStats();
  }

  async function postWithToken(path) {
    let token = localStorage.getItem("microci_admin_token") || "";
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const headers = {};
      if (token) headers.Authorization = `Bearer ${token}`;
      const res = await fetch(api(path), { method: "POST", headers });
      if (res.status !== 401) {
        if (!res.ok) throw new Error(await responseError(res));
        return res.json();
      }
      token = window.prompt("Admin token required to retry builds:") || "";
      if (!token) throw new Error("Retry cancelled");
      localStorage.setItem("microci_admin_token", token);
    }
    throw new Error("Unauthorized");
  }

  /* ------------------------------------------------------------------- UI */

  const TAB_META = {
    overview: ["Overview", "Build activity across all registered projects."],
    builds: ["Builds", "Inspect recent runs, statuses, and logs."],
    projects: ["Projects", "Repositories this runner is allowed to build."],
  };

  function setTab(tab) {
    if (!TAB_META[tab]) return;
    state.tab = tab;
    document.querySelectorAll(".view").forEach((view) => {
      view.classList.toggle("hidden", view.id !== `view-${tab}`);
    });
    document.querySelectorAll(".nav-item").forEach((item) => {
      item.classList.toggle("active", item.dataset.tab === tab);
    });
    const [title, subtitle] = TAB_META[tab];
    els.pageTitle.textContent = title;
    els.pageSubtitle.textContent = subtitle;
  }

  function selectProject(id) {
    state.filters.projectId = String(id);
    els.filterProject.value = String(id);
    renderBuildsTable();
    setTab("builds");
  }

  function bindEvents() {
    document.querySelectorAll("[data-tab]").forEach((button) => {
      button.addEventListener("click", () => setTab(button.dataset.tab));
    });
    document.querySelectorAll("[data-tab-link]").forEach((button) => {
      button.addEventListener("click", () => setTab(button.dataset.tabLink));
    });

    els.projectList.addEventListener("click", (event) => {
      const button = event.target.closest("button[data-project]");
      if (button) selectProject(button.dataset.project);
    });
    els.projectCards.addEventListener("click", (event) => {
      const button = event.target.closest("button[data-project]");
      if (button) selectProject(button.dataset.project);
    });

    const rowClick = (event) => {
      const row = event.target.closest("tr[data-build]");
      if (!row) return;
      const build = state.builds.find((item) => item.id === Number(row.dataset.build));
      if (build) openDrawer(build);
    };
    els.recentBuilds.addEventListener("click", rowClick);
    els.buildsTable.addEventListener("click", rowClick);

    els.filterProject.addEventListener("change", () => {
      state.filters.projectId = els.filterProject.value;
      renderBuildsTable();
    });
    els.filterStatus.addEventListener("change", () => {
      state.filters.status = els.filterStatus.value;
      renderBuildsTable();
    });
    els.filterSearch.addEventListener("input", () => {
      state.filters.q = els.filterSearch.value;
      renderBuildsTable();
    });

    els.drawerClose.addEventListener("click", closeDrawer);
    els.scrim.addEventListener("click", closeDrawer);
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && state.selected) closeDrawer();
    });

    els.autoscroll.addEventListener("change", () => {
      state.autoScroll = els.autoscroll.checked;
      if (state.autoScroll) renderLog(true);
    });

    els.downloadLogs.addEventListener("click", (event) => {
      if (!state.selected) event.preventDefault();
    });

    els.retryBuild.addEventListener("click", async () => {
      if (!state.selected) return;
      const id = state.selected.id;
      els.retryBuild.disabled = true;
      try {
        await postWithToken(`/api/builds/${id}/retry`);
        toast("Retry queued");
        await refreshAll();
      } catch (err) {
        toast(err.message);
      } finally {
        els.retryBuild.disabled = false;
      }
    });

    els.refresh.addEventListener("click", async () => {
      els.refresh.classList.add("spinning");
      await refreshAll();
      setTimeout(() => els.refresh.classList.remove("spinning"), 400);
    });

    els.themeToggle.addEventListener("click", () => {
      const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
      applyTheme(next);
    });
  }

  function applyTheme(theme) {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem("microci_theme", theme);
    } catch (_) {
      /* storage may be unavailable */
    }
  }

  function init() {
    const stored = (() => {
      try {
        return localStorage.getItem("microci_theme");
      } catch (_) {
        return null;
      }
    })();
    const preferred = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    applyTheme(stored || preferred);

    bindEvents();
    setTab("overview");
    refreshAll();
    connectEvents();
    setInterval(() => refreshAll({ silent: true }), POLL_MS);
  }

  init();
})();
