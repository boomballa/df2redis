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
                  size: 11
                }
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
    const entries = Object.entries(metrics);
    if (!entries.length) {
      container.innerHTML = '<p class="muted">No metrics yet.</p>';
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
        return `
        <tr class="stage-row">
          <td><strong>${name}</strong></td>
          <td><span class="badge ${badgeClass}">${status}</span></td>
          <td>${formatTime(updated)}</td>
          <td>${escapeHTML(message)}</td>
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
        const badgeClass = getBadgeClass(type);
        return `
        <div class="event-item">
          <div>
            <div class="badge ${badgeClass}">${escapeHTML(type)}</div>
            <div style="margin-top:6px;">${escapeHTML(ev.Message || ev.message || '')}</div>
          </div>
          <time>${formatTime(ev.Timestamp || ev.timestamp)}</time>
        </div>`;
      })
      .join('');
  }

  function getBadgeClass(text) {
    const lower = text.toLowerCase();
    if (lower.includes('journal') || lower.includes('incremental')) return 'badge-journal';
    if (lower.includes('established') || lower.includes('success')) return 'badge-established';
    if (lower.includes('rdb_done') || lower.includes('completed')) return 'badge-completed';
    if (lower.includes('starting') || lower.includes('handshake') || lower.includes('full_sync')) return 'badge-starting';
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

  fetchStatus();
})();
