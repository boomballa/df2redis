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

  function renderEvents(events) {
    const container = document.getElementById('events-list');
    if (!container) return;
    if (!events.length) {
      container.innerHTML = '<p class="muted">No events yet.</p>';
      return;
    }
    container.innerHTML = events
      .map(ev => {
        const type = ev.Type || ev.type || '';
        const message = ev.Message || ev.message || '';
        const badgeClass = getBadgeClass(type);
        const englishMessage = translateMessage(message);
        return `
        <div class="event-item">
          <div class="event-content">
            <span class="badge ${badgeClass}">${escapeHTML(type)}</span>
            <span class="event-separator">•</span>
            <span class="event-message">${escapeHTML(englishMessage)}</span>
          </div>
          <time>${formatTime(ev.Timestamp || ev.timestamp)}</time>
        </div>`;
      })
      .join('');
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
