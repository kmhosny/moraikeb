(function () {
  'use strict';

  const agents = {};
  let ws = null;

  const grid = document.getElementById('agent-grid');
  const emptyState = document.getElementById('empty-state');
  const connStatus = document.getElementById('connection-status');
  const agentCount = document.getElementById('agent-count');

  // State priority for sorting (higher = more important, shown first)
  const statePriority = {
    waiting: 6,
    tool_calling: 5,
    thinking: 4,
    starting: 3,
    idle: 2,
    done: 1,
  };

  function connect() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + '/ws');

    ws.onopen = function () {
      connStatus.textContent = 'Connected';
      connStatus.className = 'status-indicator connected';
    };

    ws.onclose = function () {
      connStatus.textContent = 'Disconnected';
      connStatus.className = 'status-indicator disconnected';
      setTimeout(connect, 2000);
    };

    ws.onmessage = function (e) {
      var msg = JSON.parse(e.data);
      if (msg.type === 'snapshot') {
        // Clear and rebuild
        for (var key in agents) delete agents[key];
        (msg.agents || []).forEach(function (a) {
          agents[a.session_id] = a;
        });
        renderAll();
      } else if (msg.type === 'agent_update') {
        agents[msg.agent.session_id] = msg.agent;
        renderAll();
      } else if (msg.type === 'agent_remove') {
        delete agents[msg.session_id];
        renderAll();
      }
    };
  }

  function renderAll() {
    var sorted = Object.values(agents).sort(function (a, b) {
      var pa = statePriority[a.state] || 0;
      var pb = statePriority[b.state] || 0;
      if (pa !== pb) return pb - pa;
      return (b.state_since || 0) - (a.state_since || 0);
    });

    grid.innerHTML = '';
    sorted.forEach(function (agent) {
      grid.appendChild(createCard(agent));
    });

    var count = sorted.length;
    var active = sorted.filter(function (a) { return a.state !== 'done'; }).length;
    agentCount.textContent = active + ' active / ' + count + ' total';
    emptyState.className = count > 0 ? 'hidden' : '';
  }

  function createCard(agent) {
    var card = document.createElement('div');
    card.className = 'agent-card state-' + agent.state;
    card.dataset.sessionId = agent.session_id;

    var shortId = agent.session_id.substring(0, 8);
    var project = shortPath(agent.cwd || '');
    var tool = agent.current_tool || agent.last_tool || '-';
    var elapsed = formatElapsed(agent.state_since);
    var started = agent.started_at ? formatTime(agent.started_at) : '-';

    var waitingBanner = '';
    if (agent.state === 'waiting') {
      waitingBanner = '<div class="waiting-banner">ACTION REQUIRED — Waiting for user input</div>';
    }

    var html = waitingBanner +
      '<div class="card-header">' +
      '<span class="state-pill ' + agent.state + '">' + agent.state.replace('_', ' ') + '</span>' +
      '<span class="session-id">' + shortId + '</span>' +
      '</div>' +
      '<div class="card-body">' +
      '<div class="card-row"><span class="label">Project</span><span class="value project" title="' + escapeHtml(agent.cwd || '') + '">' + escapeHtml(project) + '</span></div>' +
      '<div class="card-row"><span class="label">Tool</span><span class="value">' + escapeHtml(tool) + '</span></div>' +
      '<div class="card-row"><span class="label">In state</span><span class="value time-value" data-since="' + (agent.state_since || 0) + '">' + elapsed + '</span></div>' +
      '<div class="card-row"><span class="label">Started</span><span class="value time-value">' + started + '</span></div>' +
      '</div>';

    if (agent.last_prompt) {
      html += '<div class="prompt-preview" title="' + escapeHtml(agent.last_prompt) + '">' + escapeHtml(agent.last_prompt) + '</div>';
    }

    card.innerHTML = html;
    return card;
  }

  function shortPath(p) {
    if (!p) return '-';
    var parts = p.split('/').filter(Boolean);
    if (parts.length <= 2) return p;
    return parts.slice(-2).join('/');
  }

  function formatElapsed(since) {
    if (!since) return '-';
    var secs = Math.max(0, Math.floor(Date.now() / 1000 - since));
    if (secs < 60) return secs + 's';
    var mins = Math.floor(secs / 60);
    secs = secs % 60;
    if (mins < 60) return mins + 'm ' + secs + 's';
    var hrs = Math.floor(mins / 60);
    mins = mins % 60;
    return hrs + 'h ' + mins + 'm';
  }

  function formatTime(ts) {
    var d = new Date(ts * 1000);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  function escapeHtml(s) {
    var div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  // Update elapsed timers every second
  setInterval(function () {
    var els = document.querySelectorAll('[data-since]');
    els.forEach(function (el) {
      var since = parseFloat(el.dataset.since);
      if (since > 0) {
        el.textContent = formatElapsed(since);
      }
    });
  }, 1000);

  connect();
})();

// --- PR Review Modal ---
(function () {
  'use strict';

  var prState = {};

  function escapeHtml(s) {
    var div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  function openPRModal() {
    var modal = document.getElementById('pr-modal');
    modal.classList.remove('hidden');
    show('pr-step-url');
    hide('pr-step-repo');
    hide('pr-step-status');
    document.getElementById('pr-url-input').value = '';
    document.getElementById('pr-url-error').classList.add('hidden');
    document.getElementById('pr-url-input').focus();
    prState = {};
  }

  function closePRModal() {
    document.getElementById('pr-modal').classList.add('hidden');
  }

  function show(id) { document.getElementById(id).classList.remove('hidden'); }
  function hide(id) { document.getElementById(id).classList.add('hidden'); }

  function lookupPR() {
    var url = document.getElementById('pr-url-input').value.trim();
    if (!url) return;

    var btn = document.getElementById('pr-lookup-btn');
    btn.disabled = true;
    btn.textContent = 'Looking up...';
    hide('pr-url-error');

    fetch('/api/pr-review/lookup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: url })
    })
      .then(function (r) { return r.json(); })
      .then(function (data) {
        btn.disabled = false;
        btn.textContent = 'Look Up';

        if (data.error) {
          document.getElementById('pr-url-error').textContent = data.error;
          show('pr-url-error');
          return;
        }

        prState.url = url;
        prState.pr = data.pr;
        prState.storedRepo = data.stored_repo;
        showRepoStep();
      })
      .catch(function (err) {
        btn.disabled = false;
        btn.textContent = 'Look Up';
        document.getElementById('pr-url-error').textContent = 'Request failed: ' + err.message;
        show('pr-url-error');
      });
  }

  function showRepoStep() {
    hide('pr-step-url');
    show('pr-step-repo');
    hide('pr-repo-error');

    var pr = prState.pr;
    document.getElementById('pr-title').textContent = '#' + pr.number + ' ' + pr.title;
    document.getElementById('pr-meta').textContent =
      pr.owner + '/' + pr.repo + '  ·  ' + pr.branch + '  ·  ' + pr.commits.length + ' commit(s)';

    var desc = pr.body || '(no description)';
    document.getElementById('pr-desc').textContent =
      desc.length > 300 ? desc.substring(0, 300) + '...' : desc;

    if (prState.storedRepo) {
      show('pr-stored-repo');
      document.getElementById('pr-stored-path').textContent = prState.storedRepo.local_path;
      hide('pr-select-repo');
    } else {
      hide('pr-stored-repo');
      show('pr-select-repo');
      hide('dir-browser');
    }
  }

  function useStoredRepo() {
    prState.repoPath = prState.storedRepo.local_path;
    doStartReview();
  }

  function showBrowser() {
    hide('pr-stored-repo');
    show('pr-select-repo');
    hide('dir-browser');
  }

  function browsePath() {
    var path = document.getElementById('pr-path-input').value.trim();
    show('dir-browser');

    fetch('/api/browse?path=' + encodeURIComponent(path))
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (data.error) {
          document.getElementById('dir-path').textContent = 'Error: ' + data.error;
          document.getElementById('dir-entries').innerHTML = '';
          return;
        }
        document.getElementById('pr-path-input').value = data.path;
        renderBrowser(data);
      });
  }

  function navigateDir(path) {
    document.getElementById('pr-path-input').value = path;
    fetch('/api/browse?path=' + encodeURIComponent(path))
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (data.error) return;
        renderBrowser(data);
      });
  }

  function renderBrowser(data) {
    document.getElementById('dir-path').textContent = data.path;
    var container = document.getElementById('dir-entries');
    container.innerHTML = '';

    // Parent directory
    var parentEl = document.createElement('div');
    parentEl.className = 'dir-entry';
    parentEl.textContent = '..';
    parentEl.onclick = function () { navigateDir(data.parent); };
    container.appendChild(parentEl);

    (data.entries || []).forEach(function (e) {
      var el = document.createElement('div');
      el.className = e.is_git ? 'dir-entry dir-git' : 'dir-entry';
      var nameSpan = document.createElement('span');
      nameSpan.textContent = e.name;
      el.appendChild(nameSpan);
      if (e.is_git) {
        var badge = document.createElement('span');
        badge.className = 'git-badge';
        badge.textContent = 'git';
        el.appendChild(badge);
      }
      el.onclick = function () { navigateDir(e.path); };
      container.appendChild(el);
    });
  }

  function startPRReview() {
    var path = document.getElementById('pr-path-input').value.trim();
    if (!path) {
      document.getElementById('pr-repo-error').textContent = 'Please enter a repository path';
      show('pr-repo-error');
      return;
    }
    prState.repoPath = path;
    doStartReview();
  }

  function doStartReview() {
    hide('pr-step-repo');
    show('pr-step-status');
    var statusEl = document.getElementById('pr-status-msg');
    statusEl.textContent = 'Fetching PR, creating worktree, launching Claude Code...';
    statusEl.className = 'status-msg loading';

    fetch('/api/pr-review/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: prState.url, repo_path: prState.repoPath })
    })
      .then(function (r) { return r.json(); })
      .then(function (data) {
        if (data.error) {
          statusEl.textContent = 'Error: ' + data.error;
          statusEl.className = 'status-msg error';
          return;
        }
        statusEl.innerHTML =
          '<div class="success-icon">&#x2713;</div>' +
          '<div>Claude Code launched!</div>' +
          '<div class="status-detail">Worktree: ' + escapeHtml(data.worktree) + '</div>' +
          '<div class="status-detail">Branch: ' + escapeHtml(data.pr_branch) + '</div>';
        statusEl.className = 'status-msg success';
      })
      .catch(function (err) {
        statusEl.textContent = 'Request failed: ' + err.message;
        statusEl.className = 'status-msg error';
      });
  }

  // Handle Enter key in URL input
  document.getElementById('pr-url-input').addEventListener('keydown', function (e) {
    if (e.key === 'Enter') lookupPR();
  });

  // Handle Enter key in path input
  document.getElementById('pr-path-input').addEventListener('keydown', function (e) {
    if (e.key === 'Enter') browsePath();
  });

  // --- Settings ---

  var settingsVisible = false;
  var previousStep = null;

  function toggleSettings() {
    settingsVisible = !settingsVisible;
    if (settingsVisible) {
      // Remember which step is visible so we can restore it
      previousStep = document.querySelector('.pr-step:not(.hidden)');
      if (previousStep) previousStep.classList.add('hidden');
      show('pr-settings');
      loadSettings();
    } else {
      hide('pr-settings');
      if (previousStep) previousStep.classList.remove('hidden');
    }
  }

  function loadSettings() {
    fetch('/api/config')
      .then(function (r) { return r.json(); })
      .then(function (cfg) {
        document.getElementById('cfg-method').value = cfg.github_method || '';
        // Only set token placeholder if one is stored (value comes masked)
        var tokenInput = document.getElementById('cfg-token');
        if (cfg.github_token) {
          tokenInput.placeholder = cfg.github_token;
        } else {
          tokenInput.placeholder = 'ghp_...';
        }
        tokenInput.value = '';
        document.getElementById('cfg-claude-path').value = cfg.claude_path || '';
        updateTokenVisibility();
      });
  }

  function updateTokenVisibility() {
    var method = document.getElementById('cfg-method').value;
    if (method === 'token') {
      show('cfg-token-row');
    } else {
      hide('cfg-token-row');
    }
  }

  function saveSettings() {
    var method = document.getElementById('cfg-method').value;
    var token = document.getElementById('cfg-token').value.trim();
    var claudePath = document.getElementById('cfg-claude-path').value.trim();
    var payload = { github_method: method, claude_path: claudePath };
    // Only send token if user typed a new one
    if (token) {
      payload.github_token = token;
    }

    fetch('/api/config/save', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })
      .then(function (r) { return r.json(); })
      .then(function (data) {
        var statusEl = document.getElementById('cfg-status');
        if (data.error) {
          statusEl.textContent = 'Error: ' + data.error;
          statusEl.className = 'settings-status error';
        } else {
          statusEl.textContent = 'Settings saved!';
          statusEl.className = 'settings-status success';
          setTimeout(function () { toggleSettings(); }, 800);
        }
        show('cfg-status');
      });
  }

  // Toggle token field visibility when method changes
  document.getElementById('cfg-method').addEventListener('change', updateTokenVisibility);

  // Expose functions for onclick handlers
  window.openPRModal = openPRModal;
  window.closePRModal = closePRModal;
  window.lookupPR = lookupPR;
  window.useStoredRepo = useStoredRepo;
  window.showBrowser = showBrowser;
  window.browsePath = browsePath;
  window.startPRReview = startPRReview;
  window.toggleSettings = toggleSettings;
  window.saveSettings = saveSettings;
})();
