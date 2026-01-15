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
    const pipelineStatus = data.PipelineStatus || data.pipelineStatus || 'idle';

    updatePipeline(data);
    renderMetrics(metrics);
    renderMetricsGrid(metrics);
    renderStageTable(stages);
    updateErrorsWarnings(metrics);
    updateFlowDiagram(pipelineStatus, stages, metrics);
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
                label: function (context) {
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
                callback: function (value) {
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
      'Preparing replicator': 'Preparing replicator',
      'Connecting to Dragonfly': 'Connecting to Dragonfly',
      'Receiving RDB snapshot': 'Receiving RDB snapshot',
      'Replaying journal incrementally': 'Replaying journal incrementally',
      'Replicator stopped': 'Replicator stopped',
      'Listening to journal stream': 'Listening to journal stream',
      'success': 'success',
      'skipped': 'skipped',
      'failed': 'failed'
    };

    // Handle special patterns
    const patterns = [
      { regex: /success=(\d+)\s+skipped=(\d+)\s+failed=(\d+)/i, template: 'success=$1 skipped=$2 failed=$3' },
      { regex: /listening\s+to\s+journal\s+stream/i, template: 'Listening to journal stream' },
      { regex: /journal\s+stream\s+listening/i, template: 'Journal stream listening' }
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

  function updateFlowDiagram(pipelineStatus, stages, metrics) {
    // Get flow steps
    const flowSteps = document.querySelectorAll('.flow-step');
    if (flowSteps.length !== 4) return;

    // Normalize pipeline status
    const status = (pipelineStatus || 'idle').toLowerCase();

    // Reset all to pending
    flowSteps.forEach(step => step.setAttribute('data-status', 'pending'));

    // Update status based on current pipeline status (not history)
    if (status === 'handshake' || status === 'starting') {
      // Handshake phase: step 0 active
      flowSteps[0].setAttribute('data-status', 'active');
    } else if (status === 'full_sync') {
      // Full sync phase: step 0 completed, step 1 active
      flowSteps[0].setAttribute('data-status', 'completed');
      flowSteps[1].setAttribute('data-status', 'active');
    } else if (status === 'incremental' || status === 'journal') {
      // Incremental phase: steps 0,1 completed, step 2 active
      flowSteps[0].setAttribute('data-status', 'completed');
      flowSteps[1].setAttribute('data-status', 'completed');
      flowSteps[2].setAttribute('data-status', 'active');
    } else if (status === 'completed' || status === 'stopped') {
      // Completed: all completed
      flowSteps[0].setAttribute('data-status', 'completed');
      flowSteps[1].setAttribute('data-status', 'completed');
      flowSteps[2].setAttribute('data-status', 'completed');
      flowSteps[3].setAttribute('data-status', 'completed');
    } else if (status === 'error') {
      // Error: mark current stage as failed (use completed style for now)
      flowSteps[0].setAttribute('data-status', 'completed');
    }

    // Update RDB Snapshot keys display
    const targetKeysInitial = metrics['target.keys.initial'] || 0;
    const targetKeysCurrent = metrics['target.keys.current'] || 0;
    const rdbImportedKeys = targetKeysCurrent - targetKeysInitial;

    // Update flow step 1 (RDB Snapshot) to show imported keys
    const rdbStatusEl = flowSteps[1].querySelector('.flow-status .status-value');
    if (rdbStatusEl) {
      rdbStatusEl.textContent = formatNumber(Math.max(0, rdbImportedKeys));
    }

    // Update current stage text
    const currentStageEl = document.getElementById('current-stage');
    if (currentStageEl) {
      currentStageEl.textContent = status;
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

  // Track current viewing window for bidirectional pagination
  let windowStartOffset = 0; // Earliest line in current view
  let windowEndOffset = 0;   // Latest line in current view

  // Maximum number of log lines to keep in DOM (prevent browser slowdown)
  const MAX_LOG_LINES = 1000;

  const logViewer = document.getElementById('log-viewer');
  const logContent = document.getElementById('log-content');
  const logSearchInput = document.getElementById('log-search-input');
  const logSearchBtn = document.getElementById('log-search-btn');
  const logSearchClear = document.getElementById('log-search-clear');
  const logJumpTopBtn = document.getElementById('log-jump-top-btn');
  const logRefreshBtn = document.getElementById('log-refresh-btn');
  const logScrollBottomBtn = document.getElementById('log-scroll-bottom-btn');
  const logJumpLatestBtn = document.getElementById('log-jump-latest-btn');
  const logPauseResumeBtn = document.getElementById('log-pause-resume-btn');
  const logLoadEarlierBtn = document.getElementById('log-load-earlier-btn');
  const logLoadNewerBtn = document.getElementById('log-load-newer-btn');
  const autoRefreshIndicator = document.getElementById('auto-refresh-indicator');
  const logShownCount = document.getElementById('log-shown-count');
  const logTotalCount = document.getElementById('log-total-count');

  if (!logViewer) return;

  // Helper function to escape HTML
  function escapeHTML(str) {
    if (typeof str !== 'string') return str;
    return str.replace(/[&<>"']/g, function (c) {
      return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
    });
  }

  // Apply syntax highlighting to log line (Modern Dev theme)
  function applySyntaxHighlighting(line) {
    let highlighted = escapeHTML(line);

    // 1. Timestamp: YYYY/MM/DD HH:MM:SS
    highlighted = highlighted.replace(
      /(\d{4}\/\d{2}\/\d{2}\s+\d{2}:\d{2}:\d{2})/g,
      '<span class="log-timestamp">$1</span>'
    );

    // 2. [df2redis] or [df2redis-XXX]
    highlighted = highlighted.replace(
      /(\[df2redis[^\]]*\])/g,
      '<span class="log-app-name">$1</span>'
    );

    // 3. [FLOW-N] - with special pink background
    highlighted = highlighted.replace(
      /(\[FLOW-\d+\])/g,
      '<span class="log-flow">$1</span>'
    );

    // 4. [MODULE] - common module names
    highlighted = highlighted.replace(
      /(\[(WRITER|READER|PARSER|INIT|CONFIG|DEBUG|JOURNAL|RDB|CLUSTER|PIPELINE|CHECKER|JOURNAL-BLOB|JOURNAL-BLOB-PROCESS|INLINE-JOURNAL)\])/gi,
      '<span class="log-module">$1</span>'
    );

    // 5. Symbols - must do before search highlighting
    highlighted = highlighted.replace(/([✓✔])/g, '<span class="log-symbol-success">$1</span>');
    highlighted = highlighted.replace(/([✗✘❌])/g, '<span class="log-symbol-error">$1</span>');
    highlighted = highlighted.replace(/([⚠️⚠])/g, '<span class="log-symbol-warning">$1</span>');
    highlighted = highlighted.replace(/([→➔➜▸•])/g, '<span class="log-symbol-arrow">$1</span>');

    // 6. Numbers (standalone or with units)
    highlighted = highlighted.replace(
      /\b(\d+(?:\.\d+)?(?:ms|s|MB|KB|GB|bytes|ops\/sec|%)?)\b/g,
      '<span class="log-number">$1</span>'
    );

    // 7. Key-value pairs: key=value
    highlighted = highlighted.replace(
      /\b([a-zA-Z_][a-zA-Z0-9_]*)(=)([^\s,)]+)/g,
      '<span class="log-key">$1</span>$2<span class="log-value">$3</span>'
    );

    return highlighted;
  }

  // Highlight search keyword in element without breaking HTML structure
  function highlightTextInElement(element, keyword) {
    const searchPattern = escapeRegex(keyword);

    // Walk through all text nodes
    const walker = document.createTreeWalker(
      element,
      NodeFilter.SHOW_TEXT,
      null,
      false
    );

    const textNodesToReplace = [];
    let node;

    while (node = walker.nextNode()) {
      // Skip if already inside a highlight span
      if (node.parentElement && node.parentElement.classList.contains('log-highlight')) {
        continue;
      }

      // Check if this text node contains the keyword (case-insensitive)
      if (node.textContent.toLowerCase().includes(keyword.toLowerCase())) {
        textNodesToReplace.push(node);
      }
    }

    // Replace text nodes with highlighted versions
    textNodesToReplace.forEach(textNode => {
      const regex = new RegExp(`(${searchPattern})`, 'gi');
      const span = document.createElement('span');
      span.innerHTML = textNode.textContent.replace(regex, '<span class="log-highlight">$1</span>');
      textNode.parentNode.replaceChild(span, textNode);
    });
  }

  async function fetchLogs(offset, lines, mode = '') {
    try {
      let url = `/api/logs?offset=${offset}&lines=${lines}`;
      if (mode) {
        url += `&mode=${mode}`;
      }
      const res = await fetch(url);
      if (!res.ok) throw new Error('fetch logs failed');
      const data = await res.json();
      console.log('[Live Logs] Fetched data:', data);
      return data;
    } catch (err) {
      console.error('logs fetch error', err);
      return null;
    }
  }

  function renderLogLines(lines, mode = 'replace') {
    // mode: 'replace' (default), 'append' (add to bottom), 'prepend' (add to top)
    console.log('[Live Logs] Rendering lines:', lines ? lines.length : 0, 'mode:', mode);
    if (!lines || lines.length === 0) {
      if (mode === 'replace' && !lines) {
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

      // Apply syntax highlighting first
      let content = applySyntaxHighlighting(line);

      // Set the HTML content first
      div.innerHTML = content;

      // Then apply search highlighting using DOM manipulation to avoid breaking HTML
      if (searchKeyword) {
        if (line.toLowerCase().includes(searchKeyword.toLowerCase())) {
          div.classList.add('highlighted');
          highlightTextInElement(div, searchKeyword);
        }
      }
      fragment.appendChild(div);
    });

    if (mode === 'append') {
      // Add to bottom
      logContent.appendChild(fragment);

      // CRITICAL: Limit DOM size to prevent browser slowdown
      // When appending new lines, remove oldest lines if we exceed MAX_LOG_LINES
      const allLogLines = logContent.querySelectorAll('.log-line');
      if (allLogLines.length > MAX_LOG_LINES) {
        const toRemove = allLogLines.length - MAX_LOG_LINES;
        console.log('[Live Logs] DOM cleanup: removing', toRemove, 'oldest lines (total:', allLogLines.length, '→', MAX_LOG_LINES, ')');
        for (let i = 0; i < toRemove; i++) {
          allLogLines[i].remove();
        }
        // Adjust window start offset when removing from top
        windowStartOffset += toRemove;
      }
    } else if (mode === 'prepend') {
      // Add to top
      const firstChild = logContent.firstChild;
      logContent.insertBefore(fragment, firstChild);

      // Limit DOM size when prepending (remove from bottom)
      const allLogLines = logContent.querySelectorAll('.log-line');
      if (allLogLines.length > MAX_LOG_LINES) {
        const toRemove = allLogLines.length - MAX_LOG_LINES;
        console.log('[Live Logs] DOM cleanup: removing', toRemove, 'newest lines (total:', allLogLines.length, '→', MAX_LOG_LINES, ')');
        for (let i = allLogLines.length - 1; i >= allLogLines.length - toRemove; i--) {
          allLogLines[i].remove();
        }
        // Adjust window end offset when removing from bottom
        windowEndOffset -= toRemove;
      }
    } else {
      // Replace all
      logContent.innerHTML = '';
      logContent.appendChild(fragment);
    }

    // Update status
    logShownCount.textContent = logContent.querySelectorAll('.log-line').length;
    logTotalCount.textContent = totalLines;

    // Scroll behavior
    if (mode === 'append' || (mode === 'replace' && isAtBottom)) {
      scrollToBottom();
    }
    // For prepend, maintain scroll position (browser does this automatically)
  }

  function escapeRegex(str) {
    return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }

  async function loadInitialLogs() {
    currentOffset = 0;
    // FETCH TAIL: Use mode=tail to get the last 100 lines immediately
    const data = await fetchLogs(0, 100, 'tail');
    console.log('[Live Logs] loadInitialLogs (tail) data:', data);

    if (data) {
      totalLines = data.total;
      // IMPORTANT: When fetching tail, the server returns the last N lines.
      // We must set currentOffset to totalLines so subsequent "loadMore"
      // (or auto-refresh) fetches only *new* lines that appear after this point.
      currentOffset = totalLines;

      // Set viewing window: we're showing the last 100 lines
      windowEndOffset = totalLines;
      windowStartOffset = Math.max(0, totalLines - data.lines.length);

      renderLogLines(data.lines, 'replace');
      scrollToBottom();

      // Explicitly set "at bottom" state to true to enable auto-refresh
      isAtBottom = true;
      updateNavigationButtons();
      updateAutoRefreshState();
    } else {
      console.error('[Live Logs] No data received from fetchLogs');
      logContent.innerHTML = '<div class="log-loading" style="color:#ef4444">Failed to load logs</div>';
    }
  }

  async function loadEarlierLogs() {
    // Load 50 lines before current window start
    const startOffset = Math.max(0, windowStartOffset - 50);
    const linesToFetch = windowStartOffset - startOffset;

    if (linesToFetch <= 0) {
      console.log('[Live Logs] Already at the beginning');
      return;
    }

    console.log('[Live Logs] Loading earlier logs: offset', startOffset, 'lines', linesToFetch);
    const data = await fetchLogs(startOffset, linesToFetch);

    if (data && data.lines && data.lines.length > 0) {
      totalLines = data.total;
      windowStartOffset = startOffset;

      renderLogLines(data.lines, 'prepend');
      updateNavigationButtons();
    }
  }

  async function loadNewerLogs() {
    // Load 50 lines after current window end
    const linesToFetch = Math.min(50, totalLines - windowEndOffset);

    if (linesToFetch <= 0) {
      console.log('[Live Logs] Already at the latest');
      return;
    }

    console.log('[Live Logs] Loading newer logs: offset', windowEndOffset, 'lines', linesToFetch);
    const data = await fetchLogs(windowEndOffset, linesToFetch);

    if (data && data.lines && data.lines.length > 0) {
      totalLines = data.total;
      windowEndOffset += data.lines.length;

      renderLogLines(data.lines, 'append');
      updateNavigationButtons();
    }
  }

  async function refreshLogs() {
    // Smart incremental refresh: only fetch new lines since last check
    if (!isAtBottom || currentOffset === 0) {
      // Not at bottom or first load: reload tail
      console.log('[Live Logs] refreshLogs: reloading tail (not at bottom or first load)');
      await loadInitialLogs();
      return;
    }

    // Try incremental fetch first to detect growth rate
    console.log('[Live Logs] refreshLogs: checking for new lines from offset', currentOffset);
    const data = await fetchLogs(currentOffset, 1000); // Max 1000 new lines per refresh

    if (data) {
      const newLinesCount = data.total - currentOffset;

      // CRITICAL: Detect high-velocity log growth (RDB phase)
      // If more than 500 lines added in 5 seconds, we're falling behind
      // Switch to tail mode to show latest logs instead of appending old ones
      if (newLinesCount > 500) {
        console.log('[Live Logs] ⚡ High velocity detected:', newLinesCount, 'new lines in 5s → switching to TAIL mode');
        await loadInitialLogs(); // Reload tail to jump to latest
        return;
      }

      // Normal incremental mode (Journal phase or low activity)
      if (newLinesCount > 0 && data.lines && data.lines.length > 0) {
        console.log('[Live Logs] refreshLogs: incremental append', data.lines.length, 'new lines');
        totalLines = data.total;
        currentOffset = totalLines; // Update to latest position
        windowEndOffset = totalLines; // Update window end

        // Append new lines (will trigger DOM cleanup inside renderLogLines)
        renderLogLines(data.lines, 'append');
        scrollToBottom();
        updateNavigationButtons();
      } else {
        // No new logs, just update total count
        console.log('[Live Logs] refreshLogs: no new lines (current:', currentOffset, 'total:', data.total, ')');
        totalLines = data.total;
      }
    }
  }

  async function jumpToTop() {
    // Jump to the beginning: load first 100 lines
    currentOffset = 0;
    const data = await fetchLogs(0, 100);
    console.log('[Live Logs] jumpToTop data:', data);
    if (data) {
      totalLines = data.total;
      currentOffset = data.count;

      // Set viewing window
      windowStartOffset = 0;
      windowEndOffset = data.lines.length;

      renderLogLines(data.lines, 'replace');
      // Scroll to top
      logViewer.scrollTop = 0;
      updateNavigationButtons();
    }
  }

  async function jumpToLatest() {
    // Jump to the latest: load last 100 lines using tail mode
    const data = await fetchLogs(0, 100, 'tail'); // offset ignored when mode=tail
    console.log('[Live Logs] jumpToLatest data:', data);

    if (data) {
      totalLines = data.total;
      currentOffset = totalLines; // Set to end

      // Set viewing window
      windowEndOffset = totalLines;
      windowStartOffset = Math.max(0, totalLines - data.lines.length);

      renderLogLines(data.lines, 'replace');
      scrollToBottom();
      updateNavigationButtons();

      // Resume auto-refresh if enabled
      if (autoRefresh) {
        isAtBottom = true;
        updateAutoRefreshState();
      }
    }
  }

  function updateNavigationButtons() {
    // Show/hide "Load Earlier" button
    if (windowStartOffset > 0) {
      logLoadEarlierBtn.style.display = 'block';
      logLoadEarlierBtn.disabled = false;
    } else {
      logLoadEarlierBtn.style.display = 'none';
    }

    // Show/hide "Load Newer" button
    if (windowEndOffset < totalLines) {
      logLoadNewerBtn.style.display = 'block';
      logLoadNewerBtn.disabled = false;
    } else {
      logLoadNewerBtn.style.display = 'none';
    }

    console.log('[Live Logs] Navigation buttons updated: window[', windowStartOffset, '-', windowEndOffset, '] total:', totalLines);
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
      logPauseResumeBtn.innerHTML = '<span>⏸</span> Pause';
      if (isAtBottom) {
        startAutoRefresh();
      }
    } else {
      autoRefreshIndicator.classList.add('paused');
      autoRefreshIndicator.innerHTML = '<span class="refresh-dot"></span> Auto-refresh OFF';
      logPauseResumeBtn.innerHTML = '<span>▶</span> Resume';
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
      renderLogLines(lines, 'replace');
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
    renderLogLines(lines, 'replace');
  }

  // Event listeners
  logViewer.addEventListener('scroll', checkScrollPosition);
  logLoadEarlierBtn.addEventListener('click', loadEarlierLogs);
  logLoadNewerBtn.addEventListener('click', loadNewerLogs);
  logJumpTopBtn.addEventListener('click', jumpToTop);
  logRefreshBtn.addEventListener('click', refreshLogs);
  logScrollBottomBtn.addEventListener('click', scrollToBottom);
  logJumpLatestBtn.addEventListener('click', jumpToLatest);
  logPauseResumeBtn.addEventListener('click', toggleAutoRefresh);
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
  const checkCompareModeSelect = document.getElementById('check-compare-mode');
  const checkCompareTimesInput = document.getElementById('check-compare-times');
  const checkQPSInput = document.getElementById('check-qps');
  const checkBatchCountInput = document.getElementById('check-batch-count');
  const checkParallelInput = document.getElementById('check-parallel');

  if (!checkStartBtn) return;

  let pollTimer = null;
  let isRunning = false;

  // Helper functions
  function toNumber(val) {
    if (typeof val === 'number') return val;
    const num = Number(val);
    return Number.isNaN(num) ? 0 : num;
  }

  function formatNumber(val) {
    const num = toNumber(val);
    if (Number.isNaN(num)) return val;
    if (Math.abs(num) >= 1000) {
      return num.toLocaleString();
    }
    return num.toFixed(0);
  }

  // Start validation
  checkStartBtn.addEventListener('click', async () => {
    const compareMode = parseInt(checkCompareModeSelect.value) || 2;
    const compareTimes = parseInt(checkCompareTimesInput.value) || 3;
    const qps = parseInt(checkQPSInput.value) || 15000;
    const batchCount = parseInt(checkBatchCountInput.value) || 256;
    const parallel = parseInt(checkParallelInput.value) || 5;

    try {
      const res = await fetch('/api/check/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          compareMode,
          compareTimes,
          qps,
          batchCount,
          parallel
        })
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
    if (!status) return;

    // Update round indicator
    const roundIndicator = document.getElementById('check-round-indicator');
    if (roundIndicator && status.round && status.compareTimes) {
      roundIndicator.textContent = `Round ${status.round}/${status.compareTimes}`;
    }

    // Prefer deriving progress from checked/total to reflect the current round.
    let progressValue = 0;
    const checkedKeys = toNumber(status.checkedKeys || 0);
    const totalKeys = toNumber(status.totalKeys || 0);
    if (totalKeys > 0) {
      progressValue = Math.max(0, Math.min(1, checkedKeys / totalKeys));
    } else {
      progressValue = deriveRoundProgress(status);
    }
    const progressPercent = progressValue * 100;

    const progressBar = document.getElementById('check-progress-bar');
    if (progressBar) {
      progressBar.style.width = progressPercent + '%';
    }
    const progressLabel = document.getElementById('check-progress-percent');
    if (progressLabel) {
      progressLabel.textContent = progressPercent.toFixed(1) + '%';
    }

    // Update status text even after the task stops so the final result stays visible.
    const statusLabel = document.getElementById('check-status-text');
    if (statusLabel) {
      statusLabel.textContent = status.message || (status.running ? 'Running...' : 'Idle');
    }

    // Update stats
    document.getElementById('check-consistent-count').textContent = formatNumber(status.consistentKeys || 0);
    document.getElementById('check-inconsistent-count').textContent = formatNumber(status.inconsistentKeys || 0);
    document.getElementById('check-error-count').textContent = formatNumber(status.errorCount || 0);
    document.getElementById('check-elapsed-time').textContent = formatElapsedTime(status.elapsedSeconds || 0);
  }

  function deriveRoundProgress(status) {
    let raw = status.roundProgress;
    if (typeof raw === 'number' && Number.isFinite(raw) && raw > 0) {
      return Math.max(0, Math.min(1, raw));
    }

    const overall = toNumber(status.progress || 0);
    const totalRounds = toNumber(status.compareTimes || 0);
    const round = toNumber(status.round || 0);
    if (totalRounds > 0 && round > 0) {
      const fraction = overall * totalRounds - (round - 1);
      return Math.max(0, Math.min(1, fraction));
    }

    return Math.max(0, Math.min(1, overall));
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
