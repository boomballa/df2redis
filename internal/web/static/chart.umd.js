// Minimal chart renderer for df2redis dashboard (simple bar chart only)
(function () {
  function SimpleChart(ctx, config) {
    this.ctx = ctx.getContext('2d');
    this.data = config.data || { labels: [], datasets: [{ data: [] }] };
    this.options = config.options || {};
    this.draw();
  }

  SimpleChart.prototype.draw = function () {
    const { ctx, data } = this;
    const labels = data.labels || [];
    const values = (data.datasets && data.datasets[0] && data.datasets[0].data) || [];
    const width = ctx.canvas.width;
    const height = ctx.canvas.height;
    ctx.clearRect(0, 0, width, height);
    if (!labels.length) {
      ctx.fillStyle = '#94a3b8';
      ctx.font = '14px Inter, sans-serif';
      ctx.fillText('暂无数据', 12, height / 2);
      return;
    }
    const max = Math.max.apply(null, values.map(Math.abs)) || 1;
    const barWidth = Math.min(64, (width - 40) / labels.length - 12);
    const baseX = 40;
    const chartHeight = height - 50;
    ctx.save();
    ctx.strokeStyle = 'rgba(148,163,255,0.35)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(30, height - 30);
    ctx.lineTo(width - 10, height - 30);
    ctx.stroke();
    ctx.restore();
    labels.forEach((label, i) => {
      const value = values[i] || 0;
      const ratio = value / max;
      const barHeight = chartHeight * Math.abs(ratio);
      const x = baseX + i * (barWidth + 18);
      const y = height - 30 - barHeight;
      const gradient = ctx.createLinearGradient(0, y, 0, height - 30);
      gradient.addColorStop(0, 'rgba(99,102,241,0.9)');
      gradient.addColorStop(1, 'rgba(56,189,248,0.7)');
      ctx.fillStyle = gradient;
      ctx.beginPath();
      ctx.roundRect(x, y, barWidth, barHeight, 8);
      ctx.fill();
      ctx.fillStyle = '#cbd5f5';
      ctx.font = '12px Inter, sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(label, x + barWidth / 2, height - 12);
      ctx.fillStyle = '#e2e8f0';
      ctx.font = '12px Inter, sans-serif';
      ctx.fillText(value.toFixed(2), x + barWidth / 2, y - 6);
    });
  };

  SimpleChart.prototype.update = function () { this.draw(); };

  CanvasRenderingContext2D.prototype.roundRect = CanvasRenderingContext2D.prototype.roundRect || function (x, y, w, h, r) {
    const radius = Math.min(r, w / 2, h / 2);
    this.beginPath();
    this.moveTo(x + radius, y);
    this.arcTo(x + w, y, x + w, y + h, radius);
    this.arcTo(x + w, y + h, x, y + h, radius);
    this.arcTo(x, y + h, x, y, radius);
    this.arcTo(x, y, x + w, y, radius);
    this.closePath();
  };

  window.Chart = function (ctx, config) {
    return new SimpleChart(ctx, config);
  };
})();
