// Deployment page: durable operation polling, checklist state, and output.
(function () {
  const page = document.querySelector('.deployment-page[data-operation]');
  if (!page) return;
  const operation = page.dataset.operation;
  const log = document.getElementById('deployment-log');
  const steps = document.getElementById('deployment-steps');
  const state = document.getElementById('deployment-state');
  const description = document.getElementById('deployment-description');
  const progress = document.querySelector('#deployment-progress > span');
  const retry = document.getElementById('deployment-retry');
  const editBuild = document.getElementById('deployment-edit-build');
  const status = document.querySelector('.deployment-status');
  let seq = parseInt(page.dataset.seq || '0', 10);
  let timer = null;

  const terminal = value => ['completed', 'failed', 'rolled_back', 'rollback_failed', 'cancelled', 'interrupted'].indexOf(value) !== -1;
  const label = value => {
    const labels = {
      preflighting: 'Preparing', building: 'Building', backing_up: 'Backing up',
      cutting_over: 'Starting', verifying: 'Verifying', rolling_back: 'Rolling back',
      completed: 'Succeeded', failed: 'Failed', rolled_back: 'Rolled back',
      rollback_failed: 'Rollback failed', cancelled: 'Cancelled', interrupted: 'Interrupted'
    };
    return labels[value] || 'Queued';
  };
  const autoscroll = () => { if (log) log.scrollTop = log.scrollHeight; };

  function setStep(step) {
    if (!steps) return;
    const row = steps.querySelector('[data-step="' + (step.key || '') + '"]');
    if (!row) return;
    row.dataset.status = step.status;
    row.className = 'deployment-step step-' + step.status;
    const marker = row.querySelector('.step-marker');
    const markerText = step.status === 'succeeded' ? '✓' : step.status === 'failed' ? '!' : step.status === 'rolled_back' ? '↩' : step.status === 'running' ? '•' : step.status === 'skipped' ? '–' : '·';
    if (marker) marker.textContent = markerText;
    const stepState = row.querySelector('.step-state');
    if (stepState) stepState.textContent = step.status === 'running' ? 'Working' : step.status === 'succeeded' ? 'Done' : step.status === 'failed' ? 'Failed' : step.status === 'skipped' ? 'Skipped' : step.status === 'rolled_back' ? 'Restored' : 'Waiting';
    if (step.error) {
      let error = row.querySelector('.step-error');
      if (!error) {
        error = document.createElement('span');
        error.className = 'step-error';
        row.querySelector('.step-copy').appendChild(error);
      }
      error.textContent = step.error;
    }
  }

  function update(data) {
    if (state) state.textContent = data.label || label(data.status);
    if (description && data.error) description.textContent = data.error;
    if (status) status.className = 'deployment-status status-' + data.status;
    if (progress && Array.isArray(data.steps) && data.steps.length) {
      const complete = data.steps.filter(step => ['succeeded', 'skipped', 'rolled_back'].indexOf(step.status) !== -1).length;
      progress.style.width = Math.round(complete * 100 / data.steps.length) + '%';
    }
    (data.steps || []).forEach(setStep);
    (data.lines || []).forEach(line => {
      if (!log) return;
      log.textContent += '[' + (line.step || 'job') + '] ' + line.message + '\n';
      autoscroll();
    });
    seq = data.seq || seq;
    if (retry && ['failed', 'rolled_back', 'rollback_failed', 'interrupted'].indexOf(data.status) !== -1) retry.hidden = false;
    if (editBuild && ['failed', 'rolled_back', 'rollback_failed', 'interrupted'].indexOf(data.status) !== -1) editBuild.hidden = false;
    if (terminal(data.status)) {
      if (timer) clearTimeout(timer);
      timer = null;
    }
  }

  function poll() {
    fetch('/deployments/' + encodeURIComponent(operation) + '/status?after=' + seq)
      .then(response => { if (!response.ok) throw new Error('status ' + response.status); return response.json(); })
      .then(data => {
        update(data);
        if (!terminal(data.status)) timer = setTimeout(poll, 1500);
      })
      .catch(() => { timer = setTimeout(poll, 4000); });
  }
  poll();
})();
