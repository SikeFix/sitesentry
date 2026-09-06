/**
 * SiteSentry 前端错误上报（由 nginx sub_filter 自动注入到 </head> 前）
 *
 * 零侵入设计 —— 任何故障都不影响主站访问：
 *   - 脚本经 defer 异步加载，不阻塞渲染；
 *   - 全部逻辑 try/catch 包裹，自身异常静默；
 *   - 上报失败（接口挂了 / 网络断了）只丢弃该条，catch 吞掉；
 *   - 单页最多 MAX 条、相同错误去重，避免刷屏。
 *
 * 部署：
 *   1. 把 <REPORT_TOKEN> 替换为 SiteSentry 后台「上报令牌」生成的值；
 *   2. 放到站点根目录（如 /sentry.js）；
 *   3. 站点 vhost 中注入：
 *        sub_filter '</head>' '<script src="/sentry.js" defer></script></head>';
 *        sub_filter_once on;
 *      并在 HTML 所在 location 内加 `gzip off;`（sub_filter 不处理压缩响应）。
 */
(function () {
  'use strict';
  if (window.__ssLogInstalled) return;
  window.__ssLogInstalled = true;
  try {
    var END = 'https://status.keorigin.com/api/v1/logs';
    var TOKEN = '<REPORT_TOKEN>';
    var MAX = 10;
    var count = 0;
    var seen = {};
    var host = location.hostname || 'unknown';

    function send(level, message) {
      if (count >= MAX) return;
      var key = level + '|' + message;
      if (seen[key]) return;
      seen[key] = true;
      count++;
      try {
        var payload = JSON.stringify({
          source: host,
          level: level,
          message: String(message).slice(0, 1000),
          context: { url: location.href.slice(0, 500) }
        });
        // keepalive：页面跳转/关闭瞬间也能把请求发出去；失败静默
        fetch(END, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + TOKEN },
          body: payload,
          keepalive: true
        })['catch'](function () {});
      } catch (e) { /* 静默 */ }
    }

    // 未捕获异常 + 资源加载失败（script/link/img，capture 阶段）
    window.addEventListener('error', function (e) {
      var t = e.target;
      if (t && t !== window && (t.tagName === 'SCRIPT' || t.tagName === 'LINK' || t.tagName === 'IMG')) {
        send('warn', '资源加载失败: ' + (t.src || t.href || t.tagName));
        return;
      }
      if (e && e.message) {
        var file = (e.filename || '').replace(location.origin, '') || 'inline';
        send('error', e.message + ' @' + file + ':' + (e.lineno || 0));
      }
    }, true);

    // 未处理的 Promise 拒绝
    window.addEventListener('unhandledrejection', function (e) {
      var r = e && e.reason;
      var msg = (r instanceof Error)
        ? (r.message || 'Promise 异常')
        : String(r == null ? '未知拒绝' : r).slice(0, 300);
      send('error', '未处理的 Promise 拒绝: ' + msg);
    });
  } catch (e) { /* 静默 */ }
})();
