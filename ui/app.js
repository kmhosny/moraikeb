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
