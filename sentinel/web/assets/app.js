/* SiteSentry 哨兵 — 前端应用 (Vue 3 + Naive UI + Font Awesome) */
(function () {
  const { createApp, h } = Vue;
  const { NTag, NButton, NSpace, NPopconfirm, NSelect, NBadge } = naive;

  // ---------- API 封装 ----------
  async function api(method, url, body) {
    const opt = { method, headers: {}, credentials: 'same-origin' };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    let r;
    try {
      r = await fetch('/api' + url, opt);
    } catch (e) {
      throw new Error('网络错误：无法连接服务器');
    }
    let j = null;
    try { j = await r.json(); } catch (e) { /* 非 JSON */ }
    if (r.status === 401 && !url.startsWith('/auth/')) {
      localStorage.removeItem('ss_seen');
      location.hash = '#/login';
      throw new Error('登录已过期，请重新登录');
    }
    if (!r.ok) throw new Error((j && j.error) || ('HTTP ' + r.status));
    return j.data;
  }

  const levelTagType = lv => ({ debug: 'default', info: 'info', warn: 'warning', error: 'error', fatal: 'error' }[lv] || 'default');
  const sevTagType = sv => (sv === 'critical' ? 'error' : 'warning');

  const app = createApp({
    data() {
      return {
        user: null,
        userChecked: false,
        view: 'login',
        routeId: null,
        busy: false,
        mobileMenu: false,
        toasts: [],
        toastId: 0,

        // 主题
        themeOverrides: {
          common: {
            primaryColor: '#2563eb',
            primaryColorHover: '#3b82f6',
            primaryColorPressed: '#1d4ed8',
            primaryColorSuppl: '#3b82f6',
            borderRadius: '10px',
            borderRadiusSmall: '6px',
            fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif',
          },
        },

        // 登录
        loginTab: 'login',
        loginForm: { username: '', password: '' },
        regForm: { username: '', email: '', password: '' },

        // 仪表盘
        dash: {},
        openAnomCount: 0,

        // 目标
        targets: [],
        targetModal: { show: false, id: null, f: {} },
        detail: { points: [], stats: null },
        detailHours: 24,

        // 日志
        logs: { items: [], total: 0, page: 1, size: 30 },
        logSources: [],
        logFilter: { level: '', source: '', q: '' },
        logCtx: null,
        showLogDocs: false,
        apiBase: location.origin,

        // 异常
        anomalies: { items: [], total: 0, page: 1, size: 20 },
        anomFilter: { status: '', type: '', severity: '' },
        anomaly: null,
        busyDiag: false,

        // AI
        aiConvs: [],
        aiConvId: null,
        aiMsgs: [],
        aiInput: '',
        aiBusy: false,
        aiWithContext: true,

        // 令牌
        tokens: [],
        tokenModal: false,
        tokenName: '',
        copiedId: null,

        // 设置
        settingsTab: 'notify',
        settings: {},
        notifyForm: { default_notify_emails: '' },
        smtpForm: { smtp_host: 'smtp.feishu.cn', smtp_port: 465, smtp_mode: 'ssl', smtp_user: '', smtp_pass: '', smtp_from_name: '' },
        llmForm: { llm_base_url: '', llm_model: '', llm_api_key: '' },
        llmEnabled: true,
        aiAutoResolve: true,
        rulesForm: { log_burst_threshold: 10, latency_multiplier: 3 },
        webhookForm: { type: 'feishu', url: '' },
        webhookTypeOptions: [
          { label: '飞书机器人', value: 'feishu' },
          { label: '钉钉机器人', value: 'dingtalk' },
          { label: '企业微信机器人', value: 'wechat' },
          { label: '自定义接口（JSON）', value: 'custom' },
        ],
        llmTestResult: null,

        // 用户管理
        users: [],
        showUserModal: false,
        userModal: { username: '', email: '', password: '', role: 'user' },

        // 资料
        pwForm: { old_password: '', new_password: '' },

        // 下拉选项
        expectStatusOptions: [
          { label: '200（默认）', value: 200 }, { label: '204', value: 204 },
          { label: '301', value: 301 }, { label: '302', value: 302 }, { label: '不检查', value: 0 },
        ],
        intervalOptions: [
          { label: '30 秒', value: 30 }, { label: '1 分钟（推荐）', value: 60 },
          { label: '5 分钟', value: 300 }, { label: '10 分钟', value: 600 },
          { label: '30 分钟', value: 1800 }, { label: '1 小时', value: 3600 },
        ],
        timeoutOptions: [
          { label: '5 秒', value: 5 }, { label: '10 秒（推荐）', value: 10 },
          { label: '15 秒', value: 15 }, { label: '30 秒', value: 30 },
        ],
        levelOptions: [
          { label: '全部级别', value: '' }, { label: 'debug', value: 'debug' },
          { label: 'info', value: 'info' }, { label: 'warn', value: 'warn' },
          { label: 'error', value: 'error' }, { label: 'fatal', value: 'fatal' },
        ],
        anomStatusOptions: [
          { label: '全部状态', value: '' }, { label: '未处理', value: 'open' }, { label: '已处理', value: 'resolved' },
        ],
        anomTypeOptions: [
          { label: '全部类型', value: '' }, { label: '网站离线', value: 'check_down' },
          { label: '网站恢复', value: 'check_recovery' }, { label: '响应变慢', value: 'latency_spike' },
          { label: '日志爆发', value: 'log_burst' }, { label: '外部上报', value: 'external' },
        ],
        anomSevOptions: [
          { label: '全部级别', value: '' }, { label: '严重', value: 'critical' }, { label: '警告', value: 'warning' },
        ],
        hoursOptions: [
          { label: '最近 1 小时', value: 1 }, { label: '最近 6 小时', value: 6 },
          { label: '最近 24 小时', value: 24 }, { label: '最近 7 天', value: 168 },
        ],
        smtpModeOptions: [
          { label: 'SSL（465）', value: 'ssl' }, { label: 'STARTTLS（587）', value: 'starttls' },
        ],
        roleOptions: [
          { label: '普通用户', value: 'user' }, { label: '管理员', value: 'admin' },
        ],
        docCols: [
          { title: '字段', key: 'k', width: 130, render: r => h('code', { style: 'font-size:12px' }, r.k) },
          { title: '说明', key: 'v' },
        ],
        docRows: [
          { k: 'source', v: '来源标识（站点名/服务名），最长 128，默认 "api"' },
          { k: 'level', v: 'debug / info / warn / error / fatal' },
          { k: 'message', v: '日志内容，最长 20000 字符，必填' },
          { k: 'context', v: '可选 JSON 对象（最大 8KB），如请求参数、堆栈等' },
        ],
      };
    },

    computed: {
      avatarChar() {
        return this.user && this.user.username ? this.user.username.slice(0, 1).toUpperCase() : 'U';
      },
      menuValue() {
        if (this.view === 'target-detail') return 'targets';
        if (this.view === 'anomaly-detail') return 'anomalies';
        if (this.view === 'profile' || this.view === 'login') return null;
        return this.view;
      },
      menuOptions() {
        const icon = cls => () => h('i', { class: 'fa-solid ' + cls });
        const items = [
          { label: '仪表盘', key: 'dashboard', icon: icon('fa-gauge-high') },
          { label: '监测目标', key: 'targets', icon: icon('fa-globe') },
          { label: '日志中心', key: 'logs', icon: icon('fa-file-lines') },
          {
            label: '异常告警', key: 'anomalies', icon: icon('fa-bell'),
            suffix: this.openAnomCount > 0
              ? () => h(NBadge, { value: this.openAnomCount, max: 99, style: 'margin-left:8px' })
              : undefined,
          },
          { label: 'AI 助手', key: 'ai', icon: icon('fa-robot') },
          { label: '上报令牌', key: 'tokens', icon: icon('fa-key') },
          { label: '通知与设置', key: 'settings', icon: icon('fa-gear') },
        ];
        if (this.user && this.user.is_admin) {
          items.push({ label: '用户管理', key: 'users', icon: icon('fa-users') });
        }
        return items;
      },
      sourceOptions() {
        const opts = [{ label: '全部来源', value: '' }];
        this.logSources.forEach(s => opts.push({ label: s.source + '（' + s.count + '）', value: s.source }));
        return opts;
      },
      reversedPoints() {
        return (this.detail.points || []).slice().reverse();
      },

      // ---------- 表格列 ----------
      recentAnomCols() {
        return [
          { title: '时间', key: 'created_at', width: 140, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.created_at)) },
          { title: '类型', key: 'type', width: 100, render: r => this.typeLabel(r.type) },
          { title: '级别', key: 'severity', width: 80, render: r => h(NTag, { size: 'small', round: true, bordered: false, type: sevTagType(r.severity) }, () => this.sevLabel(r.severity)) },
          { title: '标题', key: 'title', ellipsis: { tooltip: true } },
          { title: '状态', key: 'status', width: 110, render: r => this.anomStatusTag(r) },
        ];
      },
      targetCols() {
        return [
          {
            title: '状态', key: 'status', width: 92,
            render: r => h(NTag, { size: 'small', round: true, bordered: false, type: r.status === 'up' ? 'success' : (r.status === 'down' ? 'error' : 'default') }, () => this.statusLabel(r)),
          },
          { title: '名称', key: 'name', minWidth: 120, render: r => h('a', { class: 'cell-link', onClick: () => this.go('target-detail', r.id) }, r.name) },
          { title: 'URL', key: 'url', minWidth: 180, ellipsis: { tooltip: true }, render: r => h('span', { class: 'cell-mono' }, r.url) },
          { title: '间隔', key: 'interval_sec', width: 88, render: r => this.fmtInterval(r.interval_sec) },
          {
            title: '24h 可用率', key: 'uptime_24h', width: 100,
            render: r => r.uptime_24h != null ? r.uptime_24h + '%' : h('span', { class: 'cell-muted' }, '暂无数据'),
          },
          {
            title: '平均耗时', key: 'avg_ms_24h', width: 92,
            render: r => r.avg_ms_24h != null ? Math.round(r.avg_ms_24h) + ' ms' : h('span', { class: 'cell-muted' }, '-'),
          },
          { title: '最近检查', key: 'last_check_at', width: 138, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.last_check_at)) },
          {
            title: '连续失败', key: 'fail_streak', width: 88,
            render: r => r.fail_streak > 0 ? h('span', { style: 'color:#dc2626;font-weight:700' }, r.fail_streak + ' 次') : h('span', { class: 'cell-muted' }, '-'),
          },
          {
            title: '操作', key: 'actions', width: 250,
            render: r => h(NSpace, { size: 6 }, () => [
              h(NButton, { size: 'tiny', secondary: true, type: 'primary', loading: r._busy, onClick: () => this.checkNow(r) }, () => '立即检查'),
              h(NButton, { size: 'tiny', secondary: true, onClick: () => this.openTargetModal(r) }, () => '编辑'),
              h(NButton, { size: 'tiny', secondary: true, type: r.enabled ? 'default' : 'success', onClick: () => this.toggleTarget(r) }, () => r.enabled ? '停用' : '启用'),
              h(NPopconfirm, { positiveText: '删除', negativeText: '取消', onPositiveClick: () => this.delTarget(r) }, {
                trigger: () => h(NButton, { size: 'tiny', secondary: true, type: 'error' }, () => '删除'),
                default: () => h('span', null, '确定删除该目标？其探测历史将一并删除。'),
              }),
            ]),
          },
        ];
      },
      checkCols() {
        return [
          { title: '时间', key: 'checked_at', width: 140, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.checked_at)) },
          { title: '状态', key: 'ok', width: 80, render: r => h(NTag, { size: 'small', round: true, bordered: false, type: r.ok ? 'success' : 'error' }, () => r.ok ? '正常' : '失败') },
          { title: 'HTTP 码', key: 'status_code', width: 90, render: r => r.status_code || '-' },
          { title: '耗时', key: 'ms', width: 100, render: r => h('span', { class: r.ms > 3000 ? 'cell-slow' : '' }, r.ms + ' ms') },
          { title: '错误信息', key: 'error', ellipsis: { tooltip: true }, render: r => r.error ? h('span', { class: 'cell-err' }, r.error) : h('span', { class: 'cell-muted' }, '-') },
        ];
      },
      logCols() {
        return [
          { title: '时间', key: 'created_at', width: 140, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.created_at)) },
          { title: '级别', key: 'level', width: 88, render: r => h(NTag, { size: 'small', round: true, bordered: false, type: levelTagType(r.level) }, () => r.level) },
          { title: '来源', key: 'source', width: 150, ellipsis: { tooltip: true }, render: r => h('span', { class: 'cell-mono' }, r.source) },
          {
            title: '内容', key: 'message', ellipsis: { tooltip: true },
            render: r => h('span', {
              class: r.context ? 'cell-link' : 'cell-strong',
              style: 'font-weight:' + (r.context ? '600' : '400'),
              onClick: r.context ? () => { this.logCtx = r; } : undefined,
            }, r.message),
          },
          {
            title: '上下文', key: 'context', width: 80,
            render: r => r.context
              ? h(NButton, { size: 'tiny', quaternary: true, type: 'primary', onClick: () => { this.logCtx = r; } }, () => '查看')
              : h('span', { class: 'cell-muted' }, '-'),
          },
        ];
      },
      anomCols() {
        return [
          { title: '时间', key: 'created_at', width: 140, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.created_at)) },
          { title: '类型', key: 'type', width: 100, render: r => this.typeLabel(r.type) },
          { title: '级别', key: 'severity', width: 76, render: r => h(NTag, { size: 'small', round: true, bordered: false, type: sevTagType(r.severity) }, () => this.sevLabel(r.severity)) },
          {
            title: '标题', key: 'title', ellipsis: { tooltip: true },
            render: r => h('span', null, [
              h('span', { class: 'cell-strong' }, r.title),
              r.target_name ? h('span', { class: 'cell-muted' }, '（' + r.target_name + '）') : null,
            ]),
          },
          {
            title: 'AI 诊断', key: 'llm_at', width: 96,
            render: r => r.type === 'check_recovery'
              ? h('span', { class: 'cell-muted' }, '信息事件')
              : (r.llm_at ? h(NTag, { size: 'small', round: true, bordered: false, type: 'info' }, () => '已诊断') : h('span', { class: 'cell-muted' }, '待诊断')),
          },
          { title: '状态', key: 'status', width: 110, render: r => this.anomStatusTag(r) },
        ];
      },
      recentCheckCols() {
        return [
          { title: '时间', key: 'checked_at', width: 130, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.checked_at)) },
          { title: '状态', key: 'ok', width: 70, render: r => h(NTag, { size: 'small', round: true, bordered: false, type: r.ok ? 'success' : 'error' }, () => r.ok ? '正常' : '失败') },
          { title: 'HTTP', key: 'status_code', width: 70, render: r => r.status_code || '-' },
          { title: '耗时', key: 'ms', render: r => r.ms + ' ms' },
        ];
      },
      tokenCols() {
        return [
          { title: '名称', key: 'name', render: r => h('span', { class: 'cell-strong' }, r.name) },
          {
            title: '令牌', key: 'token',
            render: r => h('span', { class: 'token-code', title: '点击复制', onClick: () => this.copyText(r.token) }, r.token),
          },
          { title: '创建时间', key: 'created_at', width: 140, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.created_at)) },
          { title: '最近使用', key: 'last_used_at', width: 140, render: r => r.last_used_at ? h('span', { class: 'cell-time' }, this.fmtT(r.last_used_at)) : h('span', { class: 'cell-muted' }, '从未使用') },
          {
            title: '操作', key: 'op', width: 90,
            render: r => h(NPopconfirm, { positiveText: '吊销', negativeText: '取消', onPositiveClick: () => this.revokeToken(r) }, {
              trigger: () => h(NButton, { size: 'tiny', secondary: true, type: 'error' }, () => '吊销'),
              default: () => h('span', null, '吊销后使用该令牌上报的网站将立即失效。'),
            }),
          },
        ];
      },
      userCols() {
        return [
          { title: 'ID', key: 'id', width: 60 },
          {
            title: '用户名', key: 'username',
            render: r => h('span', null, [
              h('span', { class: 'cell-strong' }, r.username),
              this.user && r.id === this.user.id ? h('span', { class: 'cell-muted' }, '（我）') : null,
            ]),
          },
          { title: '邮箱', key: 'email', ellipsis: { tooltip: true }, render: r => r.email || h('span', { class: 'cell-muted' }, '-') },
          { title: '角色', key: 'role', width: 88, render: r => h(NTag, { size: 'small', round: true, bordered: false, type: r.role === 'admin' ? 'info' : 'default' }, () => r.role === 'admin' ? '管理员' : '普通') },
          { title: '状态', key: 'enabled', width: 76, render: r => h(NTag, { size: 'small', round: true, bordered: false, type: r.enabled ? 'success' : 'error' }, () => r.enabled ? '启用' : '禁用') },
          { title: '目标', key: 'target_count', width: 64 },
          { title: '未处理异常', key: 'open_anomalies', width: 96 },
          { title: '最近登录', key: 'last_login_at', width: 138, render: r => r.last_login_at ? h('span', { class: 'cell-time' }, this.fmtT(r.last_login_at)) : h('span', { class: 'cell-muted' }, '从未') },
          { title: '创建时间', key: 'created_at', width: 138, render: r => h('span', { class: 'cell-time' }, this.fmtT(r.created_at)) },
          {
            title: '操作', key: 'op', width: 240,
            render: r => {
              if (this.user && r.id === this.user.id) return h('span', { class: 'cell-muted' }, '-');
              return h(NSpace, { size: 6 }, () => [
                h(NSelect, {
                  size: 'tiny', value: r.role, options: this.roleOptions, style: 'width:104px',
                  'onUpdate:value': v => this.setUserRole(r, v),
                }),
                h(NButton, { size: 'tiny', secondary: true, type: r.enabled ? 'warning' : 'success', onClick: () => this.toggleUser(r) }, () => r.enabled ? '禁用' : '启用'),
                h(NPopconfirm, { positiveText: '删除', negativeText: '取消', onPositiveClick: () => this.delUser(r) }, {
                  trigger: () => h(NButton, { size: 'tiny', secondary: true, type: 'error' }, () => '删除'),
                  default: () => h('span', null, '将删除该用户及其全部监测数据，不可恢复！'),
                }),
              ]);
            },
          },
        ];
      },
    },

    watch: {
      view(v) {
        if (v === 'login') return;
        // 会话校验未完成前不做拦截，避免首屏路由先于 /auth/me 返回而误踢回登录页
        if (!this.userChecked) return;
        if (!this.user) { this.view = 'login'; return; }
        this.viewSwitch(v);
      },
      routeId(v) {
        if (v == null) return;
        if (this.view === 'target-detail') this.loadDetail();
        if (this.view === 'anomaly-detail') this.loadAnomaly();
      },
      detailHours() { this.loadDetail(); },
    },

    mounted() {
      window.addEventListener('hashchange', this.onHashChange);
      this.onHashChange();
      this.boot();
    },

    unmounted() {
      window.removeEventListener('hashchange', this.onHashChange);
    },

    methods: {
      // ---------- 基础 ----------
      onHashChange() {
        const h = (location.hash || '#/').replace(/^#\//, '');
        const [v, id] = h.split('/');
        const map = {
          '': 'dashboard', dashboard: 'dashboard', targets: 'targets',
          'target-detail': 'target-detail', logs: 'logs', anomalies: 'anomalies',
          'anomaly-detail': 'anomaly-detail', ai: 'ai', tokens: 'tokens',
          settings: 'settings', users: 'users', profile: 'profile', login: 'login',
        };
        this.view = map[v] || 'dashboard';
        this.routeId = id ? Number(id) : null;
      },
      go(view, id) {
        location.hash = id != null ? '#/' + view + '/' + id : '#/' + view;
      },
      onMenuPick(key) {
        if (key) this.go(key);
        this.mobileMenu = false; // 移动端抽屉菜单选中后收起
      },
      // 按当前视图加载数据（watch 与 boot 首屏共用）
      viewSwitch(v) {
        switch (v) {
          case 'dashboard': this.loadDashboard(); break;
          case 'targets': this.loadTargets(); break;
          case 'target-detail': this.loadDetail(); break;
          case 'logs': this.loadLogs(1); this.loadLogSources(); break;
          case 'anomalies': this.loadAnomalies(1); break;
          case 'anomaly-detail': this.loadAnomaly(); break;
          case 'ai': this.loadAi(); break;
          case 'tokens': this.loadTokens(); break;
          case 'settings': this.loadSettings(); break;
          case 'users': this.loadUsers(); break;
          case 'profile': break;
        }
      },
      boot() {
        api('GET', '/auth/me').then(d => {
          this.user = d;
          if (this.view === 'login') {
            this.view = 'dashboard';
            if (location.hash !== '#/dashboard') location.hash = '#/dashboard';
          } else {
            // 首屏：watch 在会话校验期间被跳过，此处补加载当前视图数据
            this.viewSwitch(this.view);
          }
          this.refreshOpenCount();
          // 预加载设置（AI 助手副标题等需要模型信息）
          api('GET', '/settings').then(s => { if (this.user) this.settings = s; }).catch(() => {});
          setInterval(() => { if (this.user) this.refreshOpenCount(); }, 60000);
        }).catch(() => {
          this.user = null;
          if (location.hash !== '#/login') location.hash = '#/login';
          this.view = 'login';
        }).finally(() => { this.userChecked = true; });
      },
      toast(msg, type) {
        type = type || 'info';
        const id = ++this.toastId;
        this.toasts.push({ id, msg, type });
        setTimeout(() => { this.toasts = this.toasts.filter(t => t.id !== id); }, 4200);
      },
      toastIcon(type) {
        return {
          success: 'fa-solid fa-circle-check',
          error: 'fa-solid fa-circle-xmark',
          warning: 'fa-solid fa-triangle-exclamation',
          info: 'fa-solid fa-circle-info',
        }[type] || 'fa-solid fa-circle-info';
      },
      fmtT(t) {
        if (!t) return '-';
        const d = new Date(t);
        if (isNaN(d.getTime())) return String(t);
        const p = n => String(n).padStart(2, '0');
        return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
      },
      fmtInterval(s) {
        if (s < 60) return s + ' 秒';
        if (s < 3600) return (s / 60) + ' 分钟';
        return (s / 3600) + ' 小时';
      },
      typeLabel(t) {
        return { check_down: '网站离线', check_recovery: '网站恢复', latency_spike: '响应变慢', log_burst: '日志爆发', external: '外部上报' }[t] || t;
      },
      sevLabel(s) { return s === 'critical' ? '严重' : '警告'; },
      statusLabel(o) { const m = { up: '在线', down: '离线', unknown: '未检查' }; return (o && m[o.status]) || '-'; },
      // 异常状态标签：未处理 / AI 自动解决 / 已处理
      anomStatusTag(r) {
        if (r.status === 'open') return h(NTag, { size: 'small', round: true, bordered: false, type: 'warning' }, () => '未处理');
        if (r.ai_decision === 'auto_resolve') return h(NTag, { size: 'small', round: true, bordered: false, type: 'info' }, () => 'AI 自动解决');
        return h(NTag, { size: 'small', round: true, bordered: false, type: 'success' }, () => '已处理');
      },
      aiDecisionLabel(d) { const m = { auto_resolve: 'AI 决策：自动解决', watch: 'AI 决策：继续观察', manual: 'AI 决策：需人工处理' }; return m[d] || d; },
      renderMD(md) {
        if (!md) return '';
        let html = marked.parse(md, { breaks: true });
        html = html.replace(/<script[\s\S]*?<\/script>/gi, '');
        html = html.replace(/\son\w+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)/gi, '');
        html = html.replace(/javascript\s*:/gi, '');
        return html;
      },
      copyText(text) {
        const done = () => { this.copiedId = Date.now(); setTimeout(() => { this.copiedId = null; }, 1500); this.toast('已复制到剪贴板', 'success'); };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(done).catch(() => fallbackCopy(text, done));
        } else fallbackCopy(text, done);
      },

      // ---------- 登录/注册 ----------
      doLogin() {
        this.busy = true;
        api('POST', '/auth/login', this.loginForm).then(d => {
          this.user = d.user;
          this.toast('欢迎回来，' + d.user.username, 'success');
          this.afterAuth();
        }).catch(e => this.toast(e.message, 'error')).finally(() => { this.busy = false; });
      },
      doRegister() {
        this.busy = true;
        api('POST', '/auth/register', this.regForm).then(d => {
          this.user = d.user;
          this.toast(d.first_admin ? '注册成功！您是首位用户，已自动成为管理员' : '注册成功，欢迎使用', 'success');
          this.afterAuth();
        }).catch(e => this.toast(e.message, 'error')).finally(() => { this.busy = false; });
      },
      afterAuth() {
        location.hash = '#/dashboard';
        this.refreshOpenCount();
      },
      doLogout() {
        api('POST', '/auth/logout', {}).catch(() => {}).finally(() => {
          this.user = null;
          this.targets = []; this.dash = {};
          location.hash = '#/login';
        });
      },
      refreshOpenCount() {
        api('GET', '/anomalies?status=open&size=1').then(d => { this.openAnomCount = d.total; }).catch(() => {});
      },

      // ---------- 仪表盘 ----------
      loadDashboard() {
        api('GET', '/dashboard').then(d => {
          this.dash = d;
          this.$nextTick(() => { this.drawUptimeChart(); this.drawLevelChart(); });
        }).catch(e => this.toast(e.message, 'error'));
      },
      drawUptimeChart() {
        const el = this.$refs.uptimeChart;
        if (!el) return;
        if (this._uptimeChart) this._uptimeChart.destroy();
        const data = this.dash.uptime_7d || [];
        this._uptimeChart = new Chart(el, {
          type: 'line',
          data: {
            labels: data.map(d => d.date.slice(5)),
            datasets: [{
              label: '可用率', data: data.map(d => d.uptime),
              borderColor: '#2563eb', backgroundColor: 'rgba(37,99,235,.08)',
              fill: true, tension: .35, spanGaps: true, pointRadius: 4, pointBackgroundColor: '#2563eb',
            }],
          },
          options: {
            maintainAspectRatio: false,
            scales: { y: { min: 0, max: 100, ticks: { callback: v => v + '%' } } },
            plugins: { legend: { display: false }, tooltip: { callbacks: { label: c => c.parsed.y == null ? '无数据' : c.parsed.y + '%' } } },
          },
        });
      },
      drawLevelChart() {
        const el = this.$refs.levelChart;
        if (!el) return;
        if (this._levelChart) this._levelChart.destroy();
        const order = ['debug', 'info', 'warn', 'error', 'fatal'];
        const colors = { debug: '#94a3b8', info: '#0ea5e9', warn: '#f59e0b', error: '#ef4444', fatal: '#7f1d1d' };
        const map = {};
        (this.dash.log_levels || []).forEach(l => { map[l.level] = l.count; });
        this._levelChart = new Chart(el, {
          type: 'doughnut',
          data: {
            labels: order,
            datasets: [{ data: order.map(l => map[l] || 0), backgroundColor: order.map(l => colors[l]), borderWidth: 2 }],
          },
          options: {
            maintainAspectRatio: false, cutout: '62%',
            plugins: { legend: { position: 'right' } },
          },
        });
      },

      // ---------- 监测目标 ----------
      loadTargets() {
        api('GET', '/targets').then(d => {
          d.forEach(t => { t._busy = false; });
          this.targets = d;
        }).catch(e => this.toast(e.message, 'error'));
      },
      openTargetModal(t) {
        this.targetModal = t ? {
          show: true, id: t.id,
          f: {
            name: t.name, url: t.url, expect_status: t.expect_status, keyword: t.keyword,
            interval_sec: t.interval_sec, timeout_sec: t.timeout_sec,
            notify_emails: t.notify_emails, notify_recovery: !!t.notify_recovery, public: t.public === undefined ? true : !!t.public,
            icon: t.icon || '',
          },
        } : {
          show: true, id: null,
          f: { name: '', url: '', expect_status: 200, keyword: '', interval_sec: 60, timeout_sec: 10, notify_emails: '', notify_recovery: true, public: true, icon: '' },
        };
      },
      saveTarget() {
        const f = this.targetModal.f;
        if (!f.name || !f.url) { this.toast('请填写名称和 URL', 'warning'); return; }
        this.busy = true;
        const req = {
          name: f.name, url: f.url, expect_status: Number(f.expect_status), keyword: f.keyword,
          interval_sec: Number(f.interval_sec), timeout_sec: Number(f.timeout_sec),
          notify_emails: f.notify_emails, notify_recovery: f.notify_recovery ? 1 : 0, public: f.public ? 1 : 0,
          icon: f.icon || '',
        };
        const p = this.targetModal.id
          ? api('PUT', '/targets/' + this.targetModal.id, req)
          : api('POST', '/targets', req);
        p.then(() => {
          this.toast('目标已保存，稍后开始自动探测', 'success');
          this.targetModal.show = false;
          this.loadTargets();
        }).catch(e => this.toast(e.message, 'error')).finally(() => { this.busy = false; });
      },
      delTarget(t) {
        api('DELETE', '/targets/' + t.id, {}).then(() => {
          this.toast('已删除', 'success');
          this.loadTargets();
        }).catch(e => this.toast(e.message, 'error'));
      },
      toggleTarget(t) {
        api('PUT', '/targets/' + t.id, {
          name: t.name, url: t.url, expect_status: t.expect_status, keyword: t.keyword,
          interval_sec: t.interval_sec, timeout_sec: t.timeout_sec,
          notify_emails: t.notify_emails, notify_recovery: t.notify_recovery, public: t.public, enabled: t.enabled ? 0 : 1,
        }).then(() => {
          this.toast(t.enabled ? '已停用' : '已启用', 'success');
          this.loadTargets();
        }).catch(e => this.toast(e.message, 'error'));
      },
      checkNow(t) {
        t._busy = true;
        api('POST', '/targets/' + t.id + '/check', {}).then(() => {
          this.toast('检查完成', 'success');
          this.loadTargets();
        }).catch(e => this.toast(e.message, 'error')).finally(() => { t._busy = false; });
      },

      // ---------- 目标详情 ----------
      loadDetail() {
        const id = this.routeId;
        if (!id) return;
        Promise.all([
          api('GET', '/targets'),
          api('GET', '/targets/' + id + '/history?hours=' + this.detailHours),
        ]).then(([list, d]) => {
          const t = list.find(x => x.id === id);
          this.detail = {
            name: t ? t.name : ('目标 #' + id),
            url: t ? t.url : '',
            status: t ? t.status : null,
            points: d.points, stats: d.stats,
          };
          this.$nextTick(() => this.drawTargetChart());
        }).catch(e => this.toast(e.message, 'error'));
      },
      refreshDetail() {
        const id = this.routeId;
        if (!id) return;
        api('POST', '/targets/' + id + '/check', {}).then(() => {
          this.toast('检查完成', 'success');
          this.loadDetail();
        }).catch(e => this.toast(e.message, 'error'));
      },
      drawTargetChart() {
        const el = this.$refs.targetChart;
        if (!el) return;
        if (this._targetChart) this._targetChart.destroy();
        const pts = this.detail.points || [];
        this._targetChart = new Chart(el, {
          type: 'line',
          data: {
            labels: pts.map(p => this.fmtT(p.checked_at).slice(5)),
            datasets: [{
              label: '响应耗时 (ms)', data: pts.map(p => p.ms),
              borderColor: '#2563eb', backgroundColor: 'rgba(37,99,235,.08)',
              fill: true, tension: .25, pointRadius: pts.length > 120 ? 0 : 3,
              pointBackgroundColor: pts.map(p => p.ok ? '#16a34a' : '#dc2626'),
              spanGaps: true,
            }],
          },
          options: {
            maintainAspectRatio: false,
            scales: { x: { ticks: { maxTicksLimit: 12 } } },
            plugins: { legend: { display: false } },
          },
        });
      },

      // ---------- 日志 ----------
      loadLogs(page) {
        page = page || 1;
        const f = this.logFilter;
        const qs = '?page=' + page + '&size=' + this.logs.size +
          (f.level ? '&level=' + f.level : '') +
          (f.source ? '&source=' + encodeURIComponent(f.source) : '') +
          (f.q ? '&q=' + encodeURIComponent(f.q) : '');
        api('GET', '/logs' + qs).then(d => {
          this.logs = d;
        }).catch(e => this.toast(e.message, 'error'));
      },
      loadLogSources() {
        api('GET', '/logs/sources').then(d => { this.logSources = d; }).catch(() => {});
      },
      prettyCtx(l) {
        if (!l || !l.context) return '';
        try { return JSON.stringify(JSON.parse(l.context), null, 2); } catch (e) { return l.context; }
      },

      // ---------- 异常 ----------
      loadAnomalies(page) {
        page = page || 1;
        const f = this.anomFilter;
        const qs = '?page=' + page + '&size=' + this.anomalies.size +
          (f.status ? '&status=' + f.status : '') +
          (f.type ? '&type=' + f.type : '') +
          (f.severity ? '&severity=' + f.severity : '');
        api('GET', '/anomalies' + qs).then(d => { this.anomalies = d; }).catch(e => this.toast(e.message, 'error'));
      },
      loadAnomaly() {
        const id = this.routeId;
        if (!id) return;
        this.anomaly = null;
        api('GET', '/anomalies/' + id).then(a => { this.anomaly = a; }).catch(e => this.toast(e.message, 'error'));
      },
      resolveAnomaly() {
        api('POST', '/anomalies/' + this.routeId + '/resolve', {}).then(() => {
          this.toast('已标记为已处理', 'success');
          this.refreshOpenCount();
          this.loadAnomaly();
        }).catch(e => this.toast(e.message, 'error'));
      },
      rediagnose() {
        this.busyDiag = true;
        api('POST', '/anomalies/' + this.routeId + '/rediagnose', {})
          .then(() => {
            this.toast('AI 诊断完成', 'success');
            this.loadAnomaly();
          })
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busyDiag = false; });
      },

      // ---------- AI ----------
      loadAi() {
        api('GET', '/ai/conversations').then(cs => {
          this.aiConvs = cs;
          if (cs.length) this.openConversation(cs[0].id);
          else this.newConversation();
        }).catch(e => this.toast(e.message, 'error'));
      },
      newConversation() {
        api('POST', '/ai/conversations', { title: '新对话' }).then(() => {
          api('GET', '/ai/conversations').then(cs => {
            this.aiConvs = cs;
            if (cs.length) this.openConversation(cs[0].id);
          });
        }).catch(e => this.toast(e.message, 'error'));
      },
      openConversation(id) {
        this.aiConvId = id;
        this.aiMsgs = [];
        api('GET', '/ai/conversations/' + id + '/messages').then(ms => {
          this.aiMsgs = ms;
          this.scrollAi();
        }).catch(e => this.toast(e.message, 'error'));
      },
      sendAi() {
        const content = this.aiInput.trim();
        if (!content || this.aiBusy) return;
        if (!this.aiConvId) {
          this.newConversation();
          return;
        }
        const convId = this.aiConvId;
        this.aiMsgs.push({ role: 'user', content });
        this.aiMsgs.push({ role: 'assistant', content: '' }); // 流式回复占位（光标状态）
        const aiIdx = this.aiMsgs.length - 1;
        this.aiInput = '';
        this.aiBusy = true;
        this.scrollAi();
        // 会话切换/消息被重建时不再写入旧位置
        const live = () => this.aiConvId === convId && !!this.aiMsgs[aiIdx];
        const patch = (fn) => { if (live()) fn(this.aiMsgs[aiIdx]); };

        fetch('/api/ai/conversations/' + convId + '/messages/stream', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          credentials: 'same-origin',
          body: JSON.stringify({ content, with_context: this.aiWithContext }),
        }).then(r => {
          if (r.status === 401) {
            localStorage.removeItem('ss_seen');
            location.hash = '#/login';
            throw new Error('登录已过期，请重新登录');
          }
          if (!r.ok || !r.body) throw new Error('HTTP ' + r.status);
          const reader = r.body.getReader();
          const dec = new TextDecoder('utf-8');
          let buf = '';
          const pump = () => reader.read().then(({ done, value }) => {
            if (done) return;
            buf += dec.decode(value, { stream: true });
            let idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              const raw = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              for (const line of raw.split('\n')) {
                if (!line.startsWith('data:')) continue;
                const payload = line.slice(5).trim();
                if (!payload || payload === '[DONE]') continue;
                let j;
                try { j = JSON.parse(payload); } catch (e) { continue; }
                if (j.error) throw new Error(j.error);
                if (j.delta) {
                  patch(m => { m.content += j.delta; this.scrollAi(); });
                }
              }
            }
            return pump();
          });
          return pump().then(() => {
            patch(m => { if (!m.content) m.content = '**AI 未返回内容**（模型超时或响应为空，可重试）'; });
          });
        }).catch(e => {
          patch(m => {
            m.content = (m.content ? m.content + '\n\n' : '') + (m.content ? '（回复中断：' + e.message + '）' : '**请求失败：** ' + e.message);
          });
        }).finally(() => {
          this.aiBusy = false;
          this.scrollAi();
          api('GET', '/ai/conversations').then(cs => { this.aiConvs = cs; }).catch(() => {});
        });
      },
      scrollAi() {
        this.$nextTick(() => {
          const el = this.$refs.aiMsgsEl;
          if (el) el.scrollTop = el.scrollHeight;
        });
      },

      // ---------- 令牌 ----------
      loadTokens() {
        api('GET', '/tokens').then(d => { this.tokens = d; }).catch(e => this.toast(e.message, 'error'));
      },
      createToken() {
        const name = (this.tokenName || '').trim() || '默认令牌';
        this.busy = true;
        api('POST', '/tokens', { name }).then(d => {
          this.toast('令牌已创建', 'success');
          this.tokenModal = false;
          this.tokenName = '';
          this.loadTokens();
          setTimeout(() => this.copyText(d.token), 400);
        }).catch(e => this.toast(e.message, 'error')).finally(() => { this.busy = false; });
      },
      revokeToken(t) {
        api('DELETE', '/tokens/' + t.id, {}).then(() => {
          this.toast('已吊销', 'success');
          this.loadTokens();
        }).catch(e => this.toast(e.message, 'error'));
      },

      // ---------- 设置 ----------
      onSettingsTab(t) {
        this.settingsTab = t;
        this.loadSettings();
      },
      loadSettings() {
        api('GET', '/settings').then(s => {
          this.settings = s;
          this.notifyForm.default_notify_emails = s.default_notify_emails || '';
          this.smtpForm = {
            smtp_host: s.smtp_host || 'smtp.feishu.cn',
            smtp_port: Number(s.smtp_port || 465),
            smtp_mode: s.smtp_mode || 'ssl',
            smtp_user: s.smtp_user || '',
            smtp_pass: '',
            smtp_from_name: s.smtp_from_name || s.app_name || 'SiteSentry',
          };
          this.llmForm = {
            llm_base_url: s.llm_base_url || '',
            llm_model: s.llm_model || '',
            llm_api_key: '',
          };
          this.llmEnabled = s.llm_enabled !== '0';
          this.aiAutoResolve = s.ai_auto_resolve !== '0';
          this.rulesForm = {
            log_burst_threshold: Number(s.log_burst_threshold || 10),
            latency_multiplier: Number(s.latency_multiplier || 3),
          };
          this.webhookForm = { type: s.webhook_type || 'feishu', url: s.webhook_url || '' };
        }).catch(e => this.toast(e.message, 'error'));
      },
      saveNotify() {
        this.busy = true;
        api('POST', '/settings', { default_notify_emails: this.notifyForm.default_notify_emails })
          .then(() => this.toast('已保存', 'success'))
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      saveSmtp() {
        this.busy = true;
        const body = {
          smtp_host: this.smtpForm.smtp_host,
          smtp_port: String(this.smtpForm.smtp_port),
          smtp_mode: this.smtpForm.smtp_mode,
          smtp_user: this.smtpForm.smtp_user,
          smtp_from_name: this.smtpForm.smtp_from_name,
        };
        if (this.smtpForm.smtp_pass) body.smtp_pass = this.smtpForm.smtp_pass;
        api('POST', '/settings', body)
          .then(() => { this.toast('已保存，建议发送测试邮件验证', 'success'); this.smtpForm.smtp_pass = ''; })
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      saveLlm() {
        this.busy = true;
        const body = {
          llm_base_url: this.llmForm.llm_base_url,
          llm_model: this.llmForm.llm_model,
          llm_enabled: this.llmEnabled ? '1' : '0',
          ai_auto_resolve: this.aiAutoResolve ? '1' : '0',
        };
        if (this.llmForm.llm_api_key) body.llm_api_key = this.llmForm.llm_api_key;
        api('POST', '/settings', body)
          .then(() => this.toast('已保存', 'success'))
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      saveRules() {
        this.busy = true;
        api('POST', '/settings', {
          log_burst_threshold: String(this.rulesForm.log_burst_threshold),
          latency_multiplier: String(this.rulesForm.latency_multiplier),
        })
          .then(() => this.toast('已保存', 'success'))
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      saveWebhook() {
        this.busy = true;
        api('POST', '/settings', { webhook_type: this.webhookForm.type, webhook_url: this.webhookForm.url })
          .then(() => this.toast(this.webhookForm.url ? '已保存，可发送测试消息验证' : '已保存（Webhook 已停用）', 'success'))
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      testWebhook() {
        this.busy = true;
        api('POST', '/settings/test-webhook', {})
          .then(() => this.toast('测试消息已发送，请到群内确认', 'success'))
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      testMail(to) {
        this.busy = true;
        api('POST', '/settings/test-mail', { to: to || '' })
          .then(d => this.toast(d.sent ? '测试邮件已发送，请查收' : (d.message || '已加入发送队列'), 'success'))
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      testLlm() {
        this.llmTestResult = null;
        this.busy = true;
        api('POST', '/settings/test-llm', {})
          .then(d => { this.llmTestResult = { ok: true, text: '模型响应正常：' + d.reply }; })
          .catch(e => { this.llmTestResult = { ok: false, text: e.message }; })
          .finally(() => { this.busy = false; });
      },

      // ---------- 用户管理 ----------
      loadUsers() {
        api('GET', '/users').then(d => { this.users = d; }).catch(e => this.toast(e.message, 'error'));
      },
      doCreateUser() {
        this.busy = true;
        api('POST', '/users', this.userModal)
          .then(() => {
            this.toast('用户已创建', 'success');
            this.showUserModal = false;
            this.userModal = { username: '', email: '', password: '', role: 'user' };
            this.loadUsers();
          })
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
      setUserRole(u, role) {
        api('PUT', '/users/' + u.id, { role }).then(() => {
          this.toast('已更新', 'success');
          this.loadUsers();
        }).catch(e => this.toast(e.message, 'error'));
      },
      toggleUser(u) {
        if (u.enabled && !confirm('禁用用户「' + u.username + '」后其将无法登录，确定？')) return;
        api('PUT', '/users/' + u.id, { enabled: u.enabled ? 0 : 1 }).then(() => {
          this.toast('已更新', 'success');
          this.loadUsers();
        }).catch(e => this.toast(e.message, 'error'));
      },
      delUser(u) {
        api('DELETE', '/users/' + u.id, {}).then(() => {
          this.toast('已删除', 'success');
          this.loadUsers();
        }).catch(e => this.toast(e.message, 'error'));
      },

      // ---------- 资料 ----------
      changePw() {
        this.busy = true;
        api('POST', '/auth/password', this.pwForm)
          .then(d => {
            this.toast('密码已修改，请重新登录', 'success');
            this.pwForm = { old_password: '', new_password: '' };
            setTimeout(() => this.doLogout(), 800);
          })
          .catch(e => this.toast(e.message, 'error'))
          .finally(() => { this.busy = false; });
      },
    },
  });

  function fallbackCopy(text, done) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); done(); } catch (e) { alert('复制失败，请手动复制：\n' + text); }
    document.body.removeChild(ta);
  }

  app.use(naive);
  app.mount('#app');
})();
