import { isDiscarded } from './client.js';

export function createLoginWorkbench({ request, connected }) {
  const title = document.getElementById('login-page-title');
  const status = document.getElementById('host-oauth-status');
  let open = false;
  let revision = 0;

  function showStatus(message) {
    status.textContent = message;
    status.hidden = !message;
  }

  async function refresh() {
    if (!open || !connected()) return;
    const read = ++revision;
    try {
      const capabilities = await request('/capabilities');
      if (read !== revision) return;
      showStatus(capabilities?.login_mode === 'host_oauth' ? '' : '账号页面与当前插件版本不匹配，请重新载入管理面板。');
    } catch (error) {
      if (read === revision && !isDiscarded(error)) showStatus('登录能力读取失败，可刷新重试。');
    }
  }

  function close() {
    open = false;
    revision++;
    showStatus('');
  }

  return {
    entryResource: '/login/capabilities',
    open({ focus = true } = {}) {
      if (focus) title.focus({ preventScroll: true });
      if (open || !connected()) return;
      open = true;
      return refresh();
    },
    close,
    reset: close,
    refresh,
  };
}
