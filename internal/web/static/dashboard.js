(function () {
  const pollInterval = 5000;

  async function fetchStatus() {
    try {
      const res = await fetch('/api/status');
      if (!res.ok) throw new Error('status fetch failed');
      const data = await res.json();
      window.currentStatus = data;
      renderStatus(data);
    } catch (err) {
      console.error('status refresh error', err);
    } finally {
      setTimeout(fetchStatus, pollInterval);
    }
  }

  function renderStatus(data) {
    if (!data) return;
    const metrics = data.Metrics || data.metrics || {};
    const stages = data.Stages || data.stages || {};
    const events = data.Events || data.events || [];

    updatePipeline(data);
    renderMetrics(metrics);
    renderMetricsGrid(metrics);
    renderStageTable(stages);
    renderEvents(events);
  }

  function updatePipeline(data) {
    const statusEl = document.getElementById('pipeline-status');
    const updatedEl = document.getElementById('pipeline-updated');
    if (!statusEl || !updatedEl) return;
    const status = data.PipelineStatus || data.pipelineStatus || 'unknown';
    statusEl.textContent = status;
    statusEl.className = 'state-badge' + (status.toLowerCase() === 'failed' ? ' failed' : '');
    const ts = data.UpdatedAt || data.updatedAt;
    if (ts) {
      updatedEl.textContent = formatTime(ts);
    }
  }

  let chart;
  function renderMetrics(metrics) {
    const ctx = document.getElementById('metricsChart');
    if (!ctx || !window.Chart) return;
    const labels = Object.keys(metrics);
    const values = labels.map(key => toNumber(metrics[key]));
    if (!chart) {
      chart = new Chart(ctx, {
        type: 'bar',
        data: { labels, datasets: [{ label: 'metrics', data: values }] }
      });
      return;
    }
    chart.data.labels = labels;
    chart.data.datasets[0].data = values;
    chart.update();
  }

  function renderMetricsGrid(metrics) {
    const container = document.getElementById('metrics-list');
    if (!container) return;
    const entries = Object.entries(metrics);
    if (!entries.length) {
      container.innerHTML = '<p class="muted">暂无指标。</p>';
      return;
    }
    container.innerHTML = entries
      .map(([key, value]) => `
        <div class="metric-item">
          <div class="label">${key}</div>
          <div class="value">${formatNumber(value)}</div>
        </div>`)
      .join('');
  }

  function renderStageTable(stages) {
    const body = document.querySelector('#stages-table tbody');
    if (!body) return;
    const entries = Object.entries(stages);
    if (!entries.length) {
      body.innerHTML = '<tr><td colspan="4" class="muted">尚无阶段信息</td></tr>';
      return;
    }
    entries.sort(([a], [b]) => a.localeCompare(b));
    body.innerHTML = entries
      .map(([name, info]) => {
        const status = info.Status || info.status || '';
        const updated = info.UpdatedAt || info.updatedAt || '';
        const message = info.Message || info.message || '';
        return `
        <tr class="stage-row">
          <td>${name}</td>
          <td><span class="badge">${status}</span></td>
          <td>${formatTime(updated)}</td>
          <td>${escapeHTML(message)}</td>
        </tr>`;
      }).join('');
  }

  function renderEvents(events) {
    const container = document.getElementById('events-list');
    if (!container) return;
    if (!events.length) {
      container.innerHTML = '<p class="muted">暂无事件记录。</p>';
      return;
    }
    container.innerHTML = events
      .map(ev => `
        <div class="event-item">
          <div>
            <div class="badge" style="background:rgba(99,102,241,0.16);color:var(--accent);">${escapeHTML(ev.Type || ev.type)}</div>
            <div style="margin-top:6px;">${escapeHTML(ev.Message || ev.message || '')}</div>
          </div>
          <time>${formatTime(ev.Timestamp || ev.timestamp)}</time>
        </div>`)
      .join('');
  }

  function formatTime(ts) {
    if (!ts) return '--';
    const d = new Date(ts);
    if (Number.isNaN(d.getTime())) return ts;
    return d.toLocaleString();
  }

  function formatNumber(val) {
    const num = toNumber(val);
    if (Number.isNaN(num)) return val;
    if (Math.abs(num) >= 1000) {
      return num.toLocaleString();
    }
    return num.toFixed(4);
  }

  function toNumber(val) {
    if (typeof val === 'number') return val;
    const num = Number(val);
    return Number.isNaN(num) ? 0 : num;
  }

  function escapeHTML(str) {
    if (typeof str !== 'string') return str;
    return str.replace(/[&<>"']/g, function (c) {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
    });
  }

  fetchStatus();
})();
