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
    updateErrorsWarnings(metrics);
    updateFlowDiagram(events, stages);
    updateFlowMetrics(metrics);
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
        data: {
          labels,
          datasets: [{
            label: 'Metrics',
            data: values,
            backgroundColor: 'rgba(59, 130, 246, 0.6)',
            borderColor: 'rgba(59, 130, 246, 1)',
            borderWidth: 2,
            borderRadius: 8,
            borderSkipped: false,
          }]
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          plugins: {
            legend: {
              display: false
            },
            tooltip: {
              backgroundColor: 'rgba(30, 41, 59, 0.95)',
              titleColor: '#f1f5f9',
              bodyColor: '#94a3b8',
              borderColor: '#334155',
              borderWidth: 1,
              padding: 12,
              displayColors: false,
              callbacks: {
                label: function(context) {
                  return formatNumber(context.parsed.y);
                }
              }
            }
          },
          scales: {
            x: {
              grid: {
                display: false,
                drawBorder: false
              },
              ticks: {
                color: '#64748b',
                font: {
                  size: 10
                },
                maxRotation: 45,
                minRotation: 45,
                autoSkip: true,
                maxTicksLimit: 8
              }
            },
            y: {
              grid: {
                color: 'rgba(51, 65, 85, 0.5)',
                drawBorder: false
              },
              ticks: {
                color: '#64748b',
                font: {
                  size: 11
                },
                callback: function(value) {
                  if (value >= 1000000) {
                    return (value / 1000000).toFixed(1) + 'M';
                  }
                  if (value >= 1000) {
                    return (value / 1000).toFixed(1) + 'K';
                  }
                  return value;
                }
              }
            }
          }
        }
      });
      return;
    }
    chart.data.labels = labels;
    chart.data.datasets[0].data = values;
    chart.update('none');
  }

  function renderMetricsGrid(metrics) {
    const container = document.getElementById('metrics-list');
    if (!container) return;

    if (!Object.keys(metrics).length) {
      container.innerHTML = '<p class="muted">No metrics yet.</p>';
      return;
    }

    // Categorize metrics
    const categories = {
      'Source': [],
      'Target': [],
      'Sync Progress': [],
      'Operations': [],
      'Incremental Sync': [],
      'Checkpoint': [],
      'Flow Stats': []
    };

    Object.entries(metrics).forEach(([key, value]) => {
      if (key.startsWith('source.')) {
        categories['Source'].push([key, value]);
      } else if (key.startsWith('target.')) {
        categories['Target'].push([key, value]);
      } else if (key === 'sync.keys.applied') {
        categories['Sync Progress'].push([key, value]);
      } else if (key.startsWith('sync.incremental.ops.')) {
        categories['Operations'].push([key, value]);
      } else if (key.startsWith('sync.incremental.')) {
        categories['Incremental Sync'].push([key, value]);
      } else if (key.startsWith('checkpoint.')) {
        categories['Checkpoint'].push([key, value]);
      } else if (key.startsWith('flow.')) {
        categories['Flow Stats'].push([key, value]);
      }
    });

    let html = '';
    Object.entries(categories).forEach(([category, items]) => {
      if (items.length === 0) return;

      html += `<div class="metric-category">
        <div class="metric-category-title">${category}</div>
        <div class="metric-category-items">`;

      items.forEach(([key, value]) => {
        const displayKey = key.replace(/^(source|target|sync|checkpoint|flow)\./, '');
        let displayValue = formatNumber(value);

        // Special formatting for checkpoint timestamp
        if (key === 'checkpoint.last_saved_unix' && value > 0) {
          const date = new Date(value * 1000);
          displayValue = date.toLocaleString();
        }

        html += `
          <div class="metric-item">
            <div class="label">${displayKey}</div>
            <div class="value">${displayValue}</div>
          </div>`;
      });

      html += `</div></div>`;
    });

    container.innerHTML = html;
  }

  function renderStageTable(stages) {
    const body = document.querySelector('#stages-table tbody');
    if (!body) return;
    const entries = Object.entries(stages);
    if (!entries.length) {
      body.innerHTML = '<tr><td colspan="4" class="muted">No stage data.</td></tr>';
      return;
    }
    entries.sort(([a], [b]) => a.localeCompare(b));
    body.innerHTML = entries
      .map(([name, info]) => {
        const status = info.Status || info.status || '';
        const updated = info.UpdatedAt || info.updatedAt || '';
        const message = info.Message || info.message || '';
        const badgeClass = getBadgeClass(status);
        const englishMessage = translateMessage(message);
        return `
        <tr class="stage-row">
          <td><strong>${name}</strong></td>
          <td><span class="badge ${badgeClass}">${status}</span></td>
          <td>${formatTime(updated)}</td>
          <td>${escapeHTML(englishMessage)}</td>
        </tr>`;
      }).join('');
  }

  function updateErrorsWarnings(metrics) {
    const failedCount = metrics['sync.incremental.ops.failed'] || 0;
    const skippedCount = metrics['sync.incremental.ops.skipped'] || 0;
    const successCount = metrics['sync.incremental.ops.success'] || 0;
    const totalCount = metrics['sync.incremental.ops.total'] || 0;

    // Update counts
    const failedEl = document.getElementById('failed-count');
    const skippedEl = document.getElementById('skipped-count');
    if (failedEl) failedEl.textContent = formatNumber(failedCount);
    if (skippedEl) skippedEl.textContent = formatNumber(skippedCount);

    // Update status summary
    const statusEl = document.getElementById('status-summary');
    if (statusEl) {
      if (failedCount > 0) {
        statusEl.textContent = `${formatNumber(failedCount)} commands failed - check logs for details`;
        statusEl.style.color = '#ef4444';
      } else if (totalCount === 0) {
        statusEl.textContent = 'No operations yet';
        statusEl.style.color = '#64748b';
      } else {
        const successRate = totalCount > 0 ? ((successCount / totalCount) * 100).toFixed(1) : 100;
        statusEl.textContent = `All systems operational - ${successRate}% success rate`;
        statusEl.style.color = '#10b981';
      }
    }
  }

  function translateMessage(msg) {
    const translations = {
      '准备启动复制器': 'Preparing replicator',
      '正在连接 Dragonfly': 'Connecting to Dragonfly',
      '接收 RDB 快照': 'Receiving RDB snapshot',
      'Journal 增量重放': 'Replaying journal incrementally',
      '复制器已停止': 'Replicator stopped',
      '监听 Journal 流': 'Listening to journal stream',
      '成功': 'success',
      '跳过': 'skipped',
      '失败': 'failed'
    };

    // Handle special patterns
    const patterns = [
      { regex: /成功=(\d+)\s+跳过=(\d+)\s+失败=(\d+)/, template: 'success=$1 skipped=$2 failed=$3' },
      { regex: /监听\s+Journal\s+流/, template: 'Listening to journal stream' },
      { regex: /Journal\s+流监听/, template: 'Journal stream listening' }
    ];

    for (const { regex, template } of patterns) {
      if (regex.test(msg)) {
        return msg.replace(regex, template);
      }
    }

    // Simple replacements
    let result = msg;
    for (const [cn, en] of Object.entries(translations)) {
      result = result.replace(new RegExp(cn, 'g'), en);
    }

    return result;
  }

  function getBadgeClass(text) {
    const lower = text.toLowerCase();
    if (lower.includes('journal') || lower.includes('incremental')) return 'badge-journal';
    if (lower.includes('established') || lower.includes('success')) return 'badge-established';
    if (lower.includes('rdb_done') || lower.includes('completed')) return 'badge-completed';
    if (lower.includes('starting') || lower.includes('handshake') || lower.includes('full_sync') || lower.includes('rdb')) return 'badge-starting';
    if (lower.includes('stopped') || lower.includes('failed')) return 'badge-stopped';
    return '';
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

  function updateFlowDiagram(events, stages) {
    if (!events || events.length === 0) return;

    // Analyze events to determine flow state
    const hasHandshake = events.some(e => {
      const type = (e.Type || e.type || '').toLowerCase();
      return type.includes('handshake') || type.includes('starting');
    });

    const hasFullSync = events.some(e => {
      const type = (e.Type || e.type || '').toLowerCase();
      const msg = (e.Message || e.message || '').toLowerCase();
      return type.includes('full_sync') || msg.includes('rdb') || msg.includes('快照');
    });

    const hasIncremental = events.some(e => {
      const type = (e.Type || e.type || '').toLowerCase();
      const msg = (e.Message || e.message || '').toLowerCase();
      return type.includes('incremental') || type.includes('journal') || msg.includes('journal');
    });

    const hasStopped = events.some(e => {
      const type = (e.Type || e.type || '').toLowerCase();
      return type.includes('stopped') || type.includes('completed');
    });

    // Get flow steps
    const flowSteps = document.querySelectorAll('.flow-step');
    if (flowSteps.length !== 4) return;

    // Update status based on events
    if (hasHandshake) {
      flowSteps[0].setAttribute('data-status', 'completed');
    }

    if (hasFullSync) {
      flowSteps[1].setAttribute('data-status', 'completed');
    }

    if (hasIncremental) {
      flowSteps[1].setAttribute('data-status', 'completed');
      flowSteps[2].setAttribute('data-status', 'active');
    }

    if (hasStopped) {
      flowSteps[2].setAttribute('data-status', 'completed');
      flowSteps[3].setAttribute('data-status', 'completed');
    }

    // Update current stage text
    const currentStageEl = document.getElementById('current-stage');
    if (currentStageEl) {
      if (hasStopped) {
        currentStageEl.textContent = 'completed';
      } else if (hasIncremental) {
        currentStageEl.textContent = 'incremental';
      } else if (hasFullSync) {
        currentStageEl.textContent = 'full_sync';
      } else if (hasHandshake) {
        currentStageEl.textContent = 'handshake';
      }
    }
  }

  function updateFlowMetrics(metrics) {
    // Update all elements with data-metric attributes
    document.querySelectorAll('[data-metric]').forEach(el => {
      const metricKey = el.getAttribute('data-metric');
      const value = metrics[metricKey];
      if (value !== undefined && value !== null) {
        el.textContent = formatNumber(value);
      } else {
        el.textContent = '--';
      }
    });
  }

  fetchStatus();
})();

// Live Logs Viewer
(function () {
  let currentOffset = 0;
  let totalLines = 0;
  let searchKeyword = '';
  let autoRefresh = true;
  let refreshInterval = 5000;
  let refreshTimer = null;
  let isAtBottom = true;

  const logViewer = document.getElementById('log-viewer');
  const logContent = document.getElementById('log-content');
  const logSearchInput = document.getElementById('log-search-input');
  const logSearchBtn = document.getElementById('log-search-btn');
  const logSearchClear = document.getElementById('log-search-clear');
  const logRefreshBtn = document.getElementById('log-refresh-btn');
  const logScrollBottomBtn = document.getElementById('log-scroll-bottom-btn');
  const logSettingsBtn = document.getElementById('log-settings-btn');
  const logLoadMoreBtn = document.getElementById('log-load-more-btn');
  const autoRefreshIndicator = document.getElementById('auto-refresh-indicator');
  const logShownCount = document.getElementById('log-shown-count');
  const logTotalCount = document.getElementById('log-total-count');

  if (!logViewer) return;

  async function fetchLogs(offset, lines) {
    try {
      const res = await fetch(`/api/logs?offset=${offset}&lines=${lines}`);
      if (!res.ok) throw new Error('fetch logs failed');
      const data = await res.json();
      console.log('[Live Logs] Fetched data:', data);
      return data;
    } catch (err) {
      console.error('logs fetch error', err);
      return null;
    }
  }

  function renderLogLines(lines, append = false) {
    console.log('[Live Logs] Rendering lines:', lines ? lines.length : 0, 'append:', append);
    if (!lines || lines.length === 0) {
      if (!append) {
        logContent.innerHTML = '<div class="log-loading">No logs available</div>';
      }
      return;
    }

    const fragment = document.createDocumentFragment();
    lines.forEach(line => {
      const div = document.createElement('div');
      div.className = 'log-line';

      // Detect log level
      if (line.includes('[ERROR]')) {
        div.classList.add('log-error');
      } else if (line.includes('[WARN]')) {
        div.classList.add('log-warn');
      } else if (line.includes('[INFO]')) {
        div.classList.add('log-info');
      } else if (line.includes('[DEBUG]')) {
        div.classList.add('log-debug');
      }

      // Apply search highlighting
      let content = escapeHTML(line);
      if (searchKeyword) {
        const regex = new RegExp(`(${escapeRegex(searchKeyword)})`, 'gi');
        content = content.replace(regex, '<span class="log-highlight">$1</span>');
        if (regex.test(line)) {
          div.classList.add('highlighted');
        }
      }

      div.innerHTML = content;
      fragment.appendChild(div);
    });

    if (append) {
      logContent.appendChild(fragment);
    } else {
      logContent.innerHTML = '';
      logContent.appendChild(fragment);
    }

    // Update status
    logShownCount.textContent = logContent.querySelectorAll('.log-line').length;
    logTotalCount.textContent = totalLines;
  }

  function escapeRegex(str) {
    return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }

  async function loadInitialLogs() {
    currentOffset = 0;
    const data = await fetchLogs(0, 100);
    console.log('[Live Logs] loadInitialLogs data:', data);
    if (data) {
      totalLines = data.total;
      currentOffset = data.count;
      renderLogLines(data.lines, false);
      scrollToBottom();
      updateLoadMoreButton();
    } else {
      console.error('[Live Logs] No data received from fetchLogs');
    }
  }

  async function loadMoreLogs() {
    const data = await fetchLogs(currentOffset, 50);
    if (data) {
      totalLines = data.total;
      const oldOffset = currentOffset;
      currentOffset += data.count;
      renderLogLines(data.lines, true);
      updateLoadMoreButton();
    }
  }

  async function refreshLogs() {
    // Reload from beginning
    await loadInitialLogs();
  }

  function updateLoadMoreButton() {
    if (currentOffset >= totalLines) {
      logLoadMoreBtn.disabled = true;
      logLoadMoreBtn.textContent = 'No More Logs';
    } else {
      logLoadMoreBtn.disabled = false;
      logLoadMoreBtn.textContent = `Load 50 More Lines`;
    }
  }

  function scrollToBottom() {
    logViewer.scrollTop = logViewer.scrollHeight;
  }

  function checkScrollPosition() {
    const threshold = 50;
    const atBottom = logViewer.scrollHeight - logViewer.scrollTop - logViewer.clientHeight < threshold;

    if (atBottom !== isAtBottom) {
      isAtBottom = atBottom;
      updateAutoRefreshState();
    }
  }

  function updateAutoRefreshState() {
    if (autoRefresh && !isAtBottom) {
      // Pause auto-refresh when not at bottom
      autoRefreshIndicator.classList.add('paused');
      autoRefreshIndicator.innerHTML = '<span class="refresh-dot"></span> Auto-refresh PAUSED';
      clearInterval(refreshTimer);
      refreshTimer = null;
    } else if (autoRefresh && isAtBottom) {
      // Resume auto-refresh when at bottom
      autoRefreshIndicator.classList.remove('paused');
      autoRefreshIndicator.innerHTML = '<span class="refresh-dot"></span> Auto-refresh ON';
      if (!refreshTimer) {
        startAutoRefresh();
      }
    }
  }

  function startAutoRefresh() {
    if (refreshTimer) clearInterval(refreshTimer);
    refreshTimer = setInterval(async () => {
      if (autoRefresh && isAtBottom) {
        await refreshLogs();
      }
    }, refreshInterval);
  }

  function toggleAutoRefresh() {
    autoRefresh = !autoRefresh;
    if (autoRefresh) {
      autoRefreshIndicator.classList.remove('paused');
      autoRefreshIndicator.innerHTML = '<span class="refresh-dot"></span> Auto-refresh ON';
      if (isAtBottom) {
        startAutoRefresh();
      }
    } else {
      autoRefreshIndicator.classList.add('paused');
      autoRefreshIndicator.innerHTML = '<span class="refresh-dot"></span> Auto-refresh OFF';
      if (refreshTimer) {
        clearInterval(refreshTimer);
        refreshTimer = null;
      }
    }
  }

  function performSearch() {
    searchKeyword = logSearchInput.value.trim();
    if (searchKeyword) {
      logSearchClear.style.display = 'inline-flex';
      // Re-render current lines with highlighting
      const lines = Array.from(logContent.querySelectorAll('.log-line')).map(el => el.textContent);
      renderLogLines(lines, false);
    } else {
      clearSearch();
    }
  }

  function clearSearch() {
    searchKeyword = '';
    logSearchInput.value = '';
    logSearchClear.style.display = 'none';
    // Re-render without highlighting
    const lines = Array.from(logContent.querySelectorAll('.log-line')).map(el => el.textContent);
    renderLogLines(lines, false);
  }

  // Event listeners
  logViewer.addEventListener('scroll', checkScrollPosition);
  logLoadMoreBtn.addEventListener('click', loadMoreLogs);
  logRefreshBtn.addEventListener('click', refreshLogs);
  logScrollBottomBtn.addEventListener('click', scrollToBottom);
  logSettingsBtn.addEventListener('click', toggleAutoRefresh);
  logSearchBtn.addEventListener('click', performSearch);
  logSearchClear.addEventListener('click', clearSearch);
  logSearchInput.addEventListener('keypress', (e) => {
    if (e.key === 'Enter') {
      performSearch();
    }
  });

  // Initialize
  loadInitialLogs();
  if (autoRefresh) {
    startAutoRefresh();
  }
})();

// Data Validation (Check) Module
(function () {
  const checkStartBtn = document.getElementById('check-start-btn');
  const checkStopBtn = document.getElementById('check-stop-btn');
  const checkProgressPanel = document.getElementById('check-progress-panel');
  const checkModeRadios = document.querySelectorAll('input[name="check-mode"]');
  const samplingOptions = document.getElementById('sampling-options');

  if (!checkStartBtn) return;

  let pollTimer = null;
  let isRunning = false;

  // Toggle sampling options based on mode
  checkModeRadios.forEach(radio => {
    radio.addEventListener('change', (e) => {
      if (e.target.value === 'full') {
        samplingOptions.style.display = 'none';
      } else {
        samplingOptions.style.display = 'flex';
      }
    });
  });

  // Start validation
  checkStartBtn.addEventListener('click', async () => {
    const mode = document.querySelector('input[name="check-mode"]:checked').value;
    const sampleSize = parseInt(document.getElementById('check-sample-size').value) || 1000;
    const keyPrefix = document.getElementById('check-key-prefix').value.trim();
    const qps = parseInt(document.getElementById('check-qps').value) || 100;

    try {
      const res = await fetch('/api/check/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode, sampleSize, keyPrefix, qps })
      });

      const data = await res.json();

      if (data.success) {
        isRunning = true;
        checkStartBtn.style.display = 'none';
        checkStopBtn.style.display = 'inline-flex';
        checkProgressPanel.style.display = 'block';

        // Start polling status
        startStatusPolling();
      } else {
        alert(data.message || 'Failed to start validation');
      }
    } catch (err) {
      console.error('Start check error:', err);
      alert('Failed to start validation: ' + err.message);
    }
  });

  // Stop validation
  checkStopBtn.addEventListener('click', async () => {
    try {
      const res = await fetch('/api/check/stop', { method: 'POST' });
      const data = await res.json();

      if (data.success) {
        stopStatusPolling();
        isRunning = false;
        checkStartBtn.style.display = 'inline-flex';
        checkStopBtn.style.display = 'none';
      } else {
        alert(data.message || 'Failed to stop validation');
      }
    } catch (err) {
      console.error('Stop check error:', err);
      alert('Failed to stop validation: ' + err.message);
    }
  });

  // Poll status from server
  async function pollStatus() {
    try {
      const res = await fetch('/api/check/status');
      const status = await res.json();

      updateUI(status);

      // Stop polling if task completed
      if (!status.running && isRunning) {
        stopStatusPolling();
        isRunning = false;
        checkStartBtn.style.display = 'inline-flex';
        checkStopBtn.style.display = 'none';
      }
    } catch (err) {
      console.error('Poll status error:', err);
    }
  }

  // Update UI with status
  function updateUI(status) {
    if (!status || !status.running) return;

    // Update progress bar
    const progress = (status.progress || 0) * 100;
    document.getElementById('check-progress-bar').style.width = progress + '%';
    document.getElementById('check-progress-percent').textContent = progress.toFixed(1) + '%';

    // Update status text
    document.getElementById('check-status-text').textContent = status.message || 'Running...';

    // Update stats
    document.getElementById('check-consistent-count').textContent = formatNumber(status.consistentKeys || 0);
    document.getElementById('check-inconsistent-count').textContent = formatNumber(status.inconsistentKeys || 0);
    document.getElementById('check-error-count').textContent = formatNumber(status.errorCount || 0);
    document.getElementById('check-elapsed-time').textContent = formatElapsedTime(status.elapsedSeconds || 0);
  }

  function formatElapsedTime(seconds) {
    if (seconds < 60) {
      return seconds.toFixed(1) + 's';
    }
    const minutes = Math.floor(seconds / 60);
    const secs = Math.floor(seconds % 60);
    return `${minutes}m ${secs}s`;
  }

  function startStatusPolling() {
    if (pollTimer) clearInterval(pollTimer);
    pollTimer = setInterval(pollStatus, 1000); // Poll every second
    pollStatus(); // Initial poll
  }

  function stopStatusPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }
})();
