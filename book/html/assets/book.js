/* ============================================================
   《未来已来：AI 驱动的量化交易》 —— 在线阅读交互
   只做四件事：主题切换、移动端目录抽屉、侧栏当前项高亮、
   MathJax 配置注入。无外部依赖。
   ============================================================ */
(function () {
  'use strict';

  var LS_THEME = 'quantbot-book-theme';

  /* ---------- 1. 主题 ---------- */
  function systemDark() {
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
  }
  function applyTheme(t) {
    // t: 'light' | 'dark' | 'auto'
    var real = t === 'auto' ? (systemDark() ? 'dark' : 'light') : t;
    document.documentElement.setAttribute('data-theme', real);
    document.documentElement.setAttribute('data-theme-pref', t);
    var btn = document.getElementById('theme-btn');
    if (btn) {
      btn.textContent = real === 'dark' ? '☾ 暗色' : '☀ 亮色';
      btn.setAttribute('title',
        '当前：' + (t === 'auto' ? '跟随系统' : (real === 'dark' ? '暗色' : '亮色')) +
        '　点击切换（亮色 → 暗色 → 跟随系统）');
    }
  }
  function currentPref() {
    try { return localStorage.getItem(LS_THEME) || 'auto'; } catch (e) { return 'auto'; }
  }
  function cycleTheme() {
    var order = ['light', 'dark', 'auto'];
    var next = order[(order.indexOf(currentPref()) + 1) % order.length];
    try { localStorage.setItem(LS_THEME, next); } catch (e) { /* 忽略 */ }
    applyTheme(next);
  }

  // 尽早应用，避免亮暗闪烁
  applyTheme(currentPref());

  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('theme-btn');
    if (btn) btn.addEventListener('click', cycleTheme);
    applyTheme(currentPref());

    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: dark)');
      var onSys = function () { if (currentPref() === 'auto') applyTheme('auto'); };
      if (mq.addEventListener) mq.addEventListener('change', onSys);
      else if (mq.addListener) mq.addListener(onSys);
    }

    /* ---------- 2. 移动端目录抽屉 ---------- */
    var menu = document.getElementById('menu-btn');
    var scrim = document.querySelector('.scrim');
    function closeNav() { document.body.classList.remove('nav-open'); }
    if (menu) {
      menu.addEventListener('click', function () {
        document.body.classList.toggle('nav-open');
      });
    }
    if (scrim) scrim.addEventListener('click', closeNav);
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') closeNav();
    });
    // 点击目录里的链接后自动关闭抽屉
    document.querySelectorAll('.side a').forEach(function (a) {
      a.addEventListener('click', function () {
        if (window.matchMedia('(max-width: 62rem)').matches) closeNav();
      });
    });

    /* ---------- 3. 侧栏当前项高亮 ----------
       构建时已写入 class="current"；这里作为兜底，
       按 <body data-page> 再匹配一次，避免文件名与 data-page 不一致时漏掉。 */
    var page = document.body.getAttribute('data-page');
    if (page) {
      var hit = document.querySelector('.side a[data-page="' + page + '"]');
      if (hit && !document.querySelector('.side a.current')) hit.classList.add('current');
      if (hit) {
        var box = document.querySelector('.side');
        if (box) {
          var r = hit.getBoundingClientRect(), b = box.getBoundingClientRect();
          if (r.top < b.top || r.bottom > b.bottom) {
            box.scrollTop = hit.offsetTop - box.clientHeight / 2;
          }
        }
      }
    }

    /* ---------- 4. 脚注返回 ---------- */
    document.querySelectorAll('.fn-ref').forEach(function (a) {
      a.addEventListener('click', function () {
        var id = (a.getAttribute('href') || '').slice(1);
        var li = id && document.getElementById(id);
        if (li) li.classList.add('flash');
      });
    });
  });

  /* ---------- 5. MathJax 配置（须在 tex-svg.js 之前执行） ---------- */
  window.MathJax = {
    tex: {
      inlineMath: [['\\(', '\\)']],
      displayMath: [['\\[', '\\]']],
      processEscapes: true,
      processEnvironments: true,
      tags: 'none',
      macros: {
        E: '\\mathbb{E}',
        Prob: '\\mathbb{P}',
        R: '\\mathbb{R}',
        N: '\\mathbb{N}',
        Var: '\\operatorname{Var}',
        Cov: '\\operatorname{Cov}',
        Corr: '\\operatorname{Corr}',
        rank: '\\operatorname{rank}',
        sign: '\\operatorname{sign}',
        abs: ['\\left\\lvert #1 \\right\\rvert', 1],
        norm: ['\\left\\lVert #1 \\right\\rVert', 1],
        inner: ['\\left\\langle #1,\\,#2 \\right\\rangle', 2],
        ind: ['\\mathbf{1}\\!\\left\\{#1\\right\\}', 1],
        clamp: '\\operatorname{clamp}',
        ap: ['\\left(#1\\right)', 1]
      }
    },
    svg: { fontCache: 'global', scale: 1.02 },
    options: {
      enableMenu: true,
      menuOptions: { settings: { zoom: 'Click', zscale: '300%' } },
      skipHtmlTags: ['script', 'noscript', 'style', 'textarea', 'pre', 'code']
    },
    startup: {
      ready: function () {
        MathJax.startup.defaultReady();
        MathJax.startup.promise.then(function () {
          document.documentElement.classList.add('math-ready');
        });
      }
    }
  };
})();
