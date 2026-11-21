/* ===================================================================
   ClearLove 表白墙 2.0 前端脚本（原生 JS，无任何第三方依赖）
   功能：浏览器指纹、瀑布流无限滚动、点赞/评论/举报、发帖上传（进度条）、
        验证码、公告每日一次展示、安装向导、后台交互。
   =================================================================== */
(function () {
  'use strict';

  var CSRF = (document.querySelector('meta[name=csrf]') || {}).content || '';

  /* ---------------- 基础工具 ---------------- */

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }

  function svgIcon(path, size) {
    var s = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="' + (size || 16) +
      '" height="' + (size || 16) + '" fill="none" stroke="currentColor" stroke-width="2">' + path + '</svg>';
    var span = document.createElement('span');
    span.innerHTML = s;
    return span.firstChild;
  }

  // 头像节点：有自定义头像用 img，否则用昵称首字生成的占位圆
  function avatarEl(name, url, extraClass) {
    var klass = 'avatar' + (extraClass ? ' ' + extraClass : '');
    if (url) {
      var img = el('img', klass);
      img.src = url;
      img.alt = name || '';
      img.loading = 'lazy';
      img.decoding = 'async';
      return img;
    }
    return el('span', klass, (name || '匿')[0]);
  }

  var ICONS = {
    heart: '<path d="M12 21s-8-5.2-8-10.4C4 7 6.2 5 8.8 5c1.6 0 2.7.8 3.2 1.7C12.5 5.8 13.6 5 15.2 5 17.8 5 20 7 20 10.6 20 15.8 12 21 12 21z"/>',
    comment: '<path d="M21 12a8 8 0 1 1-3.2-6.4L21 4l-1 4.4c.6 1.1 1 2.3 1 3.6z"/>'
  };

  // 浏览器指纹（UA + 语言 + 屏幕 + canvas 渲染哈希），用于点赞去重与封禁
  var _fp = null;
  function fingerprint() {
    if (_fp) return _fp;
    try {
      var parts = [
        navigator.userAgent, navigator.language, screen.width + 'x' + screen.height,
        screen.colorDepth, new Date().getTimezoneOffset()
      ];
      var c = document.createElement('canvas');
      c.width = 200; c.height = 40;
      var ctx = c.getContext('2d');
      ctx.textBaseline = 'top';
      ctx.font = '14px Arial';
      ctx.fillStyle = '#f60';
      ctx.fillRect(0, 0, 100, 20);
      ctx.fillStyle = '#069';
      ctx.fillText('ClearLove', 2, 15);
      parts.push(c.toDataURL());
      var str = parts.join('|'), h = 5381;
      for (var i = 0; i < str.length; i++) h = ((h << 5) + h + str.charCodeAt(i)) | 0;
      _fp = 'fp' + (h >>> 0).toString(16);
    } catch (e) {
      _fp = 'fp' + Math.random().toString(16).slice(2);
    }
    return _fp;
  }

  function api(url, opts) {
    opts = opts || {};
    opts.headers = Object.assign({
      'X-CSRF-Token': CSRF,
      'X-Fingerprint': fingerprint(),
      'X-Requested-With': 'XMLHttpRequest'
    }, opts.headers || {});
    return fetch(url, opts).then(function (r) {
      return r.json().catch(function () { return { ok: r.ok }; });
    });
  }

  function toast(msg) { alert(msg); }

  /* ---------------- 开屏画面：每次会话首次进入显示约 1.5 秒 ---------------- */

  function initSplash() {
    var splash = document.getElementById('site-splash');
    if (!splash) return;
    if (document.documentElement.classList.contains('splash-skip')) {
      splash.remove();
      return;
    }
    document.body.classList.add('splash-on');
    try { sessionStorage.setItem('clv_splash_seen', '1'); } catch (e) { /* 隐私模式下忽略 */ }
    setTimeout(function () {
      splash.classList.add('is-hiding');
      document.body.classList.remove('splash-on');
      setTimeout(function () {
        if (splash.parentNode) splash.remove();
      }, 500);
    }, 1500);
  }

  /* ---------------- 公告：每天展示一次 ---------------- */

  window.dismissNotice = function () {
    var bar = document.getElementById('notice-bar');
    if (!bar) return;
    try {
      localStorage.setItem('clv_notice_' + bar.dataset.nid + '_' + new Date().toDateString(), '1');
    } catch (e) { /* 隐私模式下忽略 */ }
    bar.remove();
  };

  /* ---------------- 投币支持：1/2 枚、上滑/点击投币、砖块闪电动画 ---------------- */

  function coinIconSrc(tipped) {
    var file = tipped ? 'coin-red.svg' : 'coin.svg';
    return '/static/' + file + '?v=' + ((window.CLV && window.CLV.version) || '');
  }

  function isTippedAuthor(userID) {
    var n = Number(userID) || 0;
    return !!n && (window.CLV && (window.CLV.tippedAuthors || []).indexOf(n) >= 0);
  }

  // 已投币状态：图标换红色、禁用再次投币，并保留“已支持”提示。
  function markCoinTipped(node) {
    if (!node) return;
    node.classList.add('is-tipped');
    node.removeAttribute('data-tip');
    node.title = '已支持过该作者';
    node.setAttribute('aria-label', '已支持过该作者');
    var img = node.querySelector('img');
    if (img) img.src = coinIconSrc(true);
  }

  function initTip() {
    var modal = document.getElementById('tip-modal');
    if (!modal) return;
    var balanceText = document.getElementById('tip-balance-text');
    var stage = document.getElementById('tip-stage');
    var lightning = document.getElementById('tip-lightning');
    var carousel = modal.querySelector('.tip-carousel');
    var likeBox = document.getElementById('tip-like');
    var msg = document.getElementById('tip-msg');
    var options = Array.prototype.slice.call(modal.querySelectorAll('.tip-option'));
    var selected = 1;
    var busy = false;
    var targetPost = 0;
    var targetUser = 0;
    var balanceTenths = Number(window.CLV && window.CLV.coinBalance) || 0;
    var startX = 0, startY = 0, tracking = false;

    function fmtCoins(t) {
      t = Number(t) || 0;
      return t % 10 === 0 ? String(t / 10) : (t / 10).toFixed(1);
    }
    function renderBalance() {
      balanceText.textContent = fmtCoins(balanceTenths);
      if (window.CLV) window.CLV.coinBalance = balanceTenths;
    }
    function renderSelection() {
      var wrap = modal.querySelector('.tip-options');
      if (wrap) {
        wrap.classList.toggle('is-coin-1', selected === 1);
        wrap.classList.toggle('is-coin-2', selected === 2);
      }
      options.forEach(function (o) {
        var on = Number(o.dataset.coins) === selected;
        o.classList.toggle('is-selected', on);
        o.setAttribute('aria-pressed', on ? 'true' : 'false');
        o.classList.toggle('is-unaffordable', balanceTenths < Number(o.dataset.coins) * 10);
      });
    }
    function setSelected(n) {
      if (n !== 1 && n !== 2) return;
      selected = n;
      renderSelection();
      requestAnimationFrame(alignSelection);
    }
    function alignSelection() {
      // 小人固定在屏幕中间，砖块移动到它上方；这里只保持电弧居中
      stage.style.setProperty('--tip-target-x', '50%');
      if (lightning) lightning.style.left = '50%';
    }
    function setMsg(text, isError) {
      msg.textContent = text || '';
      msg.classList.toggle('is-error', !!isError);
    }
    function openTip(postID, userID) {
      if (!window.CLV || !window.CLV.userID) {
        window.location.href = '/login?next=' + encodeURIComponent(window.location.pathname + window.location.search);
        return;
      }
      if (!postID || !userID || Number(userID) === Number(window.CLV.userID)) return;
      targetPost = postID;
      targetUser = userID;
      busy = false;
      setSelected(1);
      if (likeBox) likeBox.checked = false;
      stage.classList.remove('is-tossing');
      clearLightning();
      setMsg('');
      balanceTenths = Number(window.CLV.coinBalance) || 0;
      renderBalance();
      modal.hidden = false;
      document.body.classList.add('tip-open');
      requestAnimationFrame(alignSelection);
      setTimeout(function () { try { stage.focus(); } catch (e) {} }, 30);
      api('/api/v1/coins').then(function (r) {
        if (r && r.ok && typeof r.balance_tenths === 'number') {
          balanceTenths = r.balance_tenths;
          renderBalance();
          renderSelection();
        }
      });
    }
    function closeTip() {
      if (busy) return;
      modal.hidden = true;
      document.body.classList.remove('tip-open');
      stage.classList.remove('is-tossing');
      clearLightning();
      setMsg('');
    }
    function clearLightning() {
      if (!lightning) return;
      lightning.classList.remove('is-active');
      lightning.innerHTML = '';
    }
    function spawnEffect() {
      stage.classList.add('is-tossing');
      var selectedCard = modal.querySelector('.tip-option[data-coins="' + selected + '"]');
      if (selectedCard) {
        selectedCard.classList.add('is-hit');
        setTimeout(function () { selectedCard.classList.remove('is-hit'); }, 620);
      }
      if (lightning) {
        lightning.innerHTML = '';
        lightning.classList.add('is-active');
        var NS = 'http://www.w3.org/2000/svg';
        var svg = document.createElementNS(NS, 'svg');
        svg.setAttribute('viewBox', '0 0 300 300');
        svg.setAttribute('class', 'tip-arc-svg');
        for (var i = 0; i < 8; i++) {
          var angle = (i * 45 + Math.random() * 24) * Math.PI / 180;
          var startR = 68;
          var pts = [[150 + Math.cos(angle) * startR, 150 + Math.sin(angle) * startR]];
          var r = startR;
          for (var k = 1; k <= 4; k++) {
            r += 17 + Math.random() * 20;
            var a = angle + (Math.random() - .5) * .42;
            pts.push([150 + Math.cos(a) * r, 150 + Math.sin(a) * r]);
          }
          var d = 'M' + pts.map(function (pt) { return pt[0].toFixed(1) + ' ' + pt[1].toFixed(1); }).join(' L');
          var glow = document.createElementNS(NS, 'path');
          glow.setAttribute('d', d); glow.setAttribute('class', 'tip-arc-glow');
          var core = document.createElementNS(NS, 'path');
          core.setAttribute('d', d); core.setAttribute('class', 'tip-arc-core');
          svg.appendChild(glow); svg.appendChild(core);
          // 短分叉，增加电弧感
          if (i % 2 === 0) {
            var start = pts[2];
            var ba = angle + (Math.random() > .5 ? .8 : -.8);
            var bd = 'M' + start[0].toFixed(1) + ' ' + start[1].toFixed(1) +
              ' L' + (start[0] + Math.cos(ba) * 32).toFixed(1) + ' ' + (start[1] + Math.sin(ba) * 32).toFixed(1) +
              ' L' + (start[0] + Math.cos(ba + .2) * 58).toFixed(1) + ' ' + (start[1] + Math.sin(ba + .2) * 58).toFixed(1);
            var branch = document.createElementNS(NS, 'path');
            branch.setAttribute('d', bd); branch.setAttribute('class', 'tip-arc-core');
            svg.appendChild(branch);
          }
        }
        lightning.appendChild(svg);
        setTimeout(function () {
          if (lightning) { lightning.classList.remove('is-active'); lightning.innerHTML = ''; }
        }, 720);
      }
      for (var j = 0; j < 4; j++) {
        var coin = document.createElement('img');
        coin.className = 'tip-coin-pop';
        coin.src = '/static/coin.svg?v=' + ((window.CLV && window.CLV.version) || '');
        coin.alt = '';
        coin.style.left = (45 + Math.random() * 10) + '%';
        coin.style.top = '27%';
        coin.style.setProperty('--dx', (Math.random() * 120 - 60).toFixed(0) + 'px');
        coin.style.setProperty('--dy', (-50 - Math.random() * 55).toFixed(0) + 'px');
        stage.appendChild(coin);
        (function (node) { setTimeout(function () { if (node.parentNode) node.remove(); }, 750); })(coin);
      }
      setTimeout(function () { stage.classList.remove('is-tossing'); }, 720);
    }
    function submitTip() {
      if (busy) return;
      var cost = selected * 10;
      if (balanceTenths < cost) {
        setMsg('硬币不足，每天会自动获得 1 枚硬币，漏天访问会补齐', true);
        return;
      }
      busy = true;
      setMsg('投币中...');
      spawnEffect();
      api('/api/v1/posts/' + targetPost + '/tip', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ coins: selected, with_like: !!(likeBox && likeBox.checked) })
      }).then(function (r) {
        if (!r || !r.ok) {
          busy = false;
          var errMsg = (r && r.msg) || '投币失败，请稍后再试';
          setMsg(errMsg, true);
          if (errMsg.indexOf('已经支持过') >= 0) {
            disableTipAuthor(targetUser);
          }
          return;
        }
        balanceTenths = r.balance_tenths;
        renderBalance();
        renderSelection();
        var xp = Number(r.sender_xp) || 0;
        setMsg('投币成功' + (xp > 0 ? '，获得 ' + xp + ' 经验' : '（今日投币经验已达上限）'));
        document.querySelectorAll('.coin-count[data-post="' + targetPost + '"]').forEach(function (node) {
          node.textContent = r.post_coins;
        });
        disableTipAuthor(targetUser);
        if (r.liked) {
          var likeBtn = document.querySelector('.like-btn[data-id="' + targetPost + '"]');
          if (likeBtn) {
            var count = likeBtn.querySelector('.like-count');
            if (count) count.textContent = r.likes;
            likeBtn.classList.add('liked');
          }
        }
        setTimeout(function () { busy = false; closeTip(); }, 760);
      }).catch(function () {
        busy = false;
        setMsg('网络异常，请稍后再试', true);
      });
    }

    options.forEach(function (o) {
      o.addEventListener('click', function () { setSelected(Number(o.dataset.coins)); });
    });
    modal.querySelectorAll('[data-tip-nav]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        setSelected(Number(btn.dataset.tipNav) < 0 ? 1 : 2);
      });
    });
    modal.querySelectorAll('[data-tip-close]').forEach(function (el) {
      el.addEventListener('click', closeTip);
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && !modal.hidden) closeTip();
    });
    stage.addEventListener('keydown', function (e) {
      if ((e.key === 'Enter' || e.key === ' ') && !modal.hidden) {
        e.preventDefault();
        submitTip();
      }
    });
    var cStartX = 0, cTracking = false;
    if (carousel) {
      carousel.addEventListener('pointerdown', function (e) {
        if (e.button !== undefined && e.button !== 0) return;
        cTracking = true;
        cStartX = e.clientX;
      });
      carousel.addEventListener('pointerup', function (e) {
        if (!cTracking) return;
        cTracking = false;
        var dx = e.clientX - cStartX;
        if (Math.abs(dx) > 34) setSelected(dx > 0 ? 1 : 2);
      });
      carousel.addEventListener('pointercancel', function () { cTracking = false; });
    }
    stage.addEventListener('pointerdown', function (e) {
      if (e.button !== undefined && e.button !== 0) return;
      tracking = true;
      startX = e.clientX;
      startY = e.clientY;
    });
    stage.addEventListener('pointerup', function (e) {
      if (!tracking) return;
      tracking = false;
      var dx = e.clientX - startX;
      var dy = e.clientY - startY;
      if (dy < -38 && Math.abs(dy) > Math.abs(dx)) {
        submitTip();
      } else if (Math.abs(dx) > 40 && Math.abs(dx) > Math.abs(dy)) {
        setSelected(dx > 0 ? 1 : 2);
      } else if (Math.abs(dx) < 9 && Math.abs(dy) < 9) {
        submitTip();
      }
    });
    stage.addEventListener('pointercancel', function () { tracking = false; });
    document.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-tip]');
      if (!btn) return;
      e.preventDefault();
      e.stopPropagation();
      openTip(btn.dataset.post, btn.dataset.user);
    });
    function disableTipAuthor(userID) {
      var n = Number(userID);
      if (!n) return;
      var list = window.CLV.tippedAuthors = window.CLV.tippedAuthors || [];
      if (list.indexOf(n) < 0) list.push(n);
      document.querySelectorAll('.coin-btn[data-user="' + n + '"], .coin-stat[data-user="' + n + '"]')
        .forEach(markCoinTipped);
    }
    (window.CLV.tippedAuthors || []).forEach(function (id) { disableTipAuthor(id); });
    window.addEventListener('resize', alignSelection);
    renderBalance();
    renderSelection();
  }

  function initNotice() {
    var bar = document.getElementById('notice-bar');
    if (!bar) return;
    var key = 'clv_notice_' + bar.dataset.nid + '_' + new Date().toDateString();
    var seen = false;
    try { seen = localStorage.getItem(key) === '1'; } catch (e) { }
    if (seen) bar.remove();
  }

  /* ---------------- 瀑布流：无限滚动 + 懒加载 ---------------- */

  function mediaEl(src, isVideo) {
    if (isVideo) {
      var v = el('video', 'video');
      v.src = src; v.controls = true; v.preload = 'metadata';
      return v;
    }
    var img = el('img', 'media');
    img.loading = 'lazy';       // 懒加载
    img.decoding = 'async';
    img.alt = '帖子图片';
    img.src = src;
    return img;
  }

  function timeText(s) {
    if (!s) return '';
    var d = new Date(s);
    if (isNaN(d.getTime())) return s;
    function p(n) { return n < 10 ? '0' + n : '' + n; }
    return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
  }

  function cardEl(p) {
    var art = el('article', 'card post-card');

    var head = el('div', 'card-head');
    var av = avatarEl(p.nickname, p.avatar);
    if (p.user_id) {
      var avLink = el('a', 'avatar-link');
      avLink.href = '/u/' + p.user_id;
      avLink.title = '查看主页';
      avLink.appendChild(av);
      head.appendChild(avLink);
    } else {
      head.appendChild(av);
    }
    var meta = el('div', 'meta');
    // 匿名帖 user_id=0 无个人主页，渲染为纯文本避免点击 404
    var nick;
    if (p.user_id) {
      nick = el('a', 'nickname', p.nickname || '匿名');
      nick.href = '/u/' + p.user_id;
    } else {
      nick = el('span', 'nickname', p.nickname || '匿名');
    }
    var nickRow = el('div', 'nick-row');
    nickRow.appendChild(nick);
    var levelNum = parseInt(p.level, 10) || 0;
    if (p.user_id && levelNum > 0) {
      nickRow.appendChild(el('span', 'level-badge lv' + levelNum, 'Lv' + levelNum));
    } else {
      nickRow.appendChild(el('span', 'level-badge anon', '匿名'));
    }
    // 帖子标识（内置「管理员」+ 插件 badge 钩子写入）
    var badges = p.badges || [];
    for (var bi = 0; bi < badges.length; bi++) {
      nickRow.appendChild(el('span', 'post-badge', badges[bi]));
    }
    meta.appendChild(nickRow);
    var t = el('time', '', timeText(p.created_at));
    meta.appendChild(t);
    head.appendChild(meta);
    art.appendChild(head);

    var link = el('a', 'card-link');
    link.href = '/post/' + p.id;
    if (p.topic) {
      var chip = el('span', 'chip', '# ' + p.topic);
      chip.style.marginBottom = '6px';
      link.appendChild(chip);
    }
    link.appendChild(el('p', 'content', p.content || ''));
    if (p.images && p.images.length) {
      var grid = el('div', 'media-grid media-' + Math.min(p.images.length, 4));
      for (var i = 0; i < p.images.length; i++) grid.appendChild(mediaEl(p.images[i], false));
      link.appendChild(grid);
    }
    if (p.videos && p.videos.length) {
      for (var j = 0; j < p.videos.length; j++) link.appendChild(mediaEl(p.videos[j], true));
    }
    art.appendChild(link);

    // 最新4条评论（点击卡片区域进入详情）
    var cmts = p.comments || [];
    if (cmts.length) {
      var box = el('div', 'preview-comments');
      for (var k = 0; k < cmts.length; k++) {
        var row = el('p');
        row.appendChild(el('span', 'pc-nick', (cmts[k].nickname || '匿名') + '：'));
        row.appendChild(document.createTextNode(cmts[k].content || ''));
        box.appendChild(row);
      }
      if (p.comment_count > cmts.length) {
        var all = el('a', 'view-all', '查看全部 ' + p.comment_count + ' 条评论');
        all.href = '/post/' + p.id;
        box.appendChild(all);
      }
      art.appendChild(box);
    }

    var acts = el('div', 'card-actions');
    var like = el('button', 'act like-btn');
    like.type = 'button';
    like.dataset.id = p.id;
    like.appendChild(svgIcon(ICONS.heart, 17));
    like.appendChild(el('span', 'like-count', p.likes || 0));
    acts.appendChild(like);
    var cm = el('span', 'act');
    cm.appendChild(svgIcon(ICONS.comment, 17));
    cm.appendChild(document.createTextNode(p.comment_count || 0));
    acts.appendChild(cm);
    var coinCount = Number(p.coins) || 0;
    var tipped = isTippedAuthor(p.user_id);
    var canTip = window.CLV && window.CLV.userID && p.user_id && Number(p.user_id) !== Number(window.CLV.userID) && !tipped;
    var coinEl = canTip ? el('button', 'act coin-btn') : el('span', 'act coin-stat');
    if (canTip) {
      coinEl.type = 'button';
      coinEl.dataset.tip = '1';
      coinEl.dataset.post = p.id;
      coinEl.dataset.user = p.user_id;
      coinEl.title = '投币支持';
      coinEl.setAttribute('aria-label', '投币支持');
    } else if (tipped) {
      coinEl.classList.add('is-tipped');
      coinEl.dataset.user = p.user_id;
      coinEl.title = '已支持过该作者';
      coinEl.setAttribute('aria-label', '已支持过该作者');
    } else {
      coinEl.title = '帖子投币数';
    }
    var coinImg = el('img');
    coinImg.src = coinIconSrc(tipped);
    coinImg.alt = '';
    coinEl.appendChild(coinImg);
    var coinNum = el('span', 'coin-count', coinCount);
    coinNum.dataset.post = p.id;
    coinEl.appendChild(coinNum);
    acts.appendChild(coinEl);
    art.appendChild(acts);

    // 点击卡片主体进入详情
    art.addEventListener('click', function (ev) {
      if (ev.target.closest('button, a, video')) return;
      window.location.href = '/post/' + p.id;
    });
    applyCardHooks(art, p);
    return art;
  }

  /* 插件卡片扩展点：CLV.hooks.cardHtml / cardEl / afterRender
     插件通过注入脚本注册（见 docs/PLUGIN-APP.md 第 9.7 节） */
  function applyCardHooks(art, p) {
    var hooks = window.CLV && window.CLV.hooks;
    if (!hooks) return;
    (hooks.cardHtml || []).forEach(function (fn) {
      try {
        var extra = fn('', p);
        if (extra) {
          var box = el('div', 'clv-plugin-card-extra');
          box.innerHTML = extra;
          art.appendChild(box);
        }
      } catch (e) { }
    });
    (hooks.cardEl || []).forEach(function (fn) { try { fn(art, p); } catch (e) { } });
    (hooks.afterRender || []).forEach(function (fn) { try { fn(art, p); } catch (e) { } });
  }

  function initMasonry() {
    var box = document.getElementById('masonry');
    if (!box) return;
    var state = document.getElementById('load-state');
    var sentinel = document.getElementById('sentinel');
    var topic = box.dataset.topic || '';
    var last = 0, loading = false, done = false;
    var io = null;
    var PAGE = 10;

    // 页面底部是否已进入预加载范围
    function nearBottom() {
      return window.innerHeight + window.scrollY > document.body.offsetHeight - 500;
    }

    // 重新观察哨兵。observe() 会立即按"当前"相交状态回调一次，而这正是关键：
    // IntersectionObserver 只在相交状态**变化**时回调，若一批数据插入后哨兵仍在
    // 预加载范围内，状态没变就不会再回调，页面就会永远卡在"加载中..."。
    // 内容不足以填满屏幕时（话题栏收起后页面变短尤其明显），还要靠它续加载。
    function kick() {
      if (io && sentinel && sentinel.parentNode) {
        io.unobserve(sentinel);
        io.observe(sentinel);
      } else if (!io && nearBottom()) {
        load();
      }
    }

    function finish(msg) {
      done = true;
      state.textContent = msg;
      if (sentinel) sentinel.remove();
      if (io) { io.disconnect(); io = null; }
    }

    function load() {
      if (loading || done) return;
      loading = true;
      state.textContent = '加载中...';
      api('/api/v1/posts?before=' + last + '&limit=' + PAGE + '&topic=' + encodeURIComponent(topic))
        .then(function (r) {
          loading = false;
          if (!r || !r.ok) {
            state.textContent = '加载失败，请刷新页面重试';
            return;
          }
          var posts = r.posts || [];
          posts.forEach(function (p) { box.appendChild(cardEl(p)); });
          if (posts.length) last = posts[posts.length - 1].id;
          if (posts.length < PAGE) {
            finish(last ? '已经到底啦' : '还没有帖子，点击右下角发布第一条');
            return;
          }
          // 还有更多：清掉加载提示，并让哨兵重新判断是否需要立即续加载
          state.textContent = '';
          kick();
        })
        .catch(function () {
          loading = false;
          state.textContent = '网络异常，请稍后重试';
        });
    }

    if (sentinel && 'IntersectionObserver' in window) {
      io = new IntersectionObserver(function (entries) {
        if (entries[0].isIntersecting) load();
      }, { rootMargin: '400px' });
      io.observe(sentinel);
    } else {
      // 兜底：滚动监听（首屏内容不足时同样需要主动续加载）
      window.addEventListener('scroll', function () { if (nearBottom()) load(); });
    }
    load();
  }

  /* ---------------- 点赞 / 举报（事件委托） ---------------- */

  document.addEventListener('click', function (ev) {
    var like = ev.target.closest('.like-btn');
    if (like) {
      ev.preventDefault();
      var id = like.dataset.id;
      api('/api/v1/posts/' + id + '/like', { method: 'POST' })
        .then(function (r) {
          if (!r || !r.ok) { toast((r && r.msg) || '操作失败'); return; }
          var span = like.querySelector('.like-count');
          if (span) span.textContent = r.likes;
          like.classList.toggle('liked', !!r.liked);
        });
      return;
    }
    var rep = ev.target.closest('.report-btn');
    if (rep) {
      ev.preventDefault();
      var reason = prompt('请填写举报理由（例如：广告、辱骂、违规内容）');
      if (!reason) return;
      var fd = new FormData();
      fd.append('post_id', rep.dataset.id);
      fd.append('reason', reason);
      api('/api/v1/reports', { method: 'POST', body: fd }).then(function (r) {
        toast((r && r.msg) || (r.ok ? '举报已提交' : '举报失败'));
      });
    }
  });

  /* ---------------- 评论表单 ---------------- */

  function initCommentForm() {
    var form = document.getElementById('comment-form');
    if (!form) return;
    var list = document.getElementById('comment-list');
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var fd = new FormData(form);
      fd.append('post_id', form.dataset.post);
      var btn = form.querySelector('button[type=submit]');
      btn.disabled = true;
      api('/api/v1/comments', { method: 'POST', body: fd }).then(function (r) {
        btn.disabled = false;
        if (!r || !r.ok) { toast((r && r.msg) || '评论失败'); return; }
        var empty = list.querySelector('.empty');
        if (empty) empty.remove();
        var item = el('div', 'comment-item');
        item.appendChild(el('span', 'avatar small', (fd.get('nickname') || '匿')[0]));
        var body = el('div', 'comment-body');
        body.appendChild(el('span', 'nickname', fd.get('nickname') || '匿名'));
        body.appendChild(el('p', '', fd.get('content')));
        item.appendChild(body);
        list.appendChild(item);
        form.reset();
      });
    });
  }

  /* ---------------- 发帖页：验证码 + 上传进度 ---------------- */

  function loadCaptcha() {
    var box = document.getElementById('captcha-box');
    if (!box) return;
    api('/api/captcha').then(function (r) {
      if (!r || !r.id) return;
      box.innerHTML = r.svg;
      box.dataset.id = r.id;
    });
  }

  /* ---------------- 发帖 AI 预审动画 ---------------- */

  // 上传完成后进入 AI 审核阶段时展示，分阶段提示文案
  function initAiScan() {
    var overlay = document.getElementById('ai-scan');
    if (!overlay) return null;
    var sub = document.getElementById('ai-scan-sub');
    var steps = ['正在提取内容特征', '正在比对社区守则', 'AI 正在给出审核结论', '即将完成'];
    var idx = 0, timer = null;
    return {
      show: function () {
        idx = 0;
        if (sub) sub.textContent = steps[0];
        overlay.hidden = false;
        if (timer) clearInterval(timer);
        timer = setInterval(function () {
          idx = Math.min(idx + 1, steps.length - 1);
          if (sub) sub.textContent = steps[idx];
        }, 2200);
      },
      hide: function () {
        if (timer) { clearInterval(timer); timer = null; }
        overlay.hidden = true;
      }
    };
  }

  function initCompose() {
    var form = document.getElementById('compose-form');
    if (!form) return;
    var aiScan = initAiScan(); // AI 预审开启时才有该元素
    var errBox = document.getElementById('compose-err');
    function showErr(msg) {
      if (errBox) { errBox.textContent = msg; errBox.hidden = false; }
      else toast(msg);
    }

    /* ---- 管理员专享昵称：命中时需验证后台账号 ---- */
    var adminNicks = (form.dataset.adminNicks || '').split(',').map(function (s) {
      return s.trim().toLowerCase();
    }).filter(function (s) { return s; });
    var adminAuth = { user: '', pass: '' }; // 本次发布携带的后台凭证

    function isReservedNick(name) {
      return adminNicks.indexOf(String(name || '').trim().toLowerCase()) >= 0;
    }

    /* ---- 媒体上传：拖拽（PC）+ 点击选择（移动端/PC） ---- */
    var dropZone = document.getElementById('drop-zone');
    var input = document.getElementById('media-input');
    var previews = document.getElementById('media-previews');
    var maxImages = Number(dropZone && dropZone.dataset.maxImages) || 15;
    var maxVideos = Number(dropZone && dropZone.dataset.maxVideos) || 3;
    var MAX_IMG = 10 * 1024 * 1024, MAX_VID = 70 * 1024 * 1024;
    var selected = []; // { file, url, type }

    function humanSize(n) {
      if (n >= 1048576) return (n / 1048576).toFixed(1) + 'MB';
      return Math.max(1, Math.round(n / 1024)) + 'KB';
    }
    function countType(t) {
      var n = 0;
      for (var i = 0; i < selected.length; i++) if (selected[i].type === t) n++;
      return n;
    }
    function renderPreviews() {
      if (!previews) return;
      previews.innerHTML = '';
      selected.forEach(function (it, idx) {
        var item = el('div', 'media-thumb');
        if (it.type === 'image') {
          var im = el('img');
          im.src = it.url;
          im.alt = it.file.name;
          item.appendChild(im);
        } else {
          item.appendChild(el('div', 'media-file-icon', '视频'));
        }
        var rm = el('button', 'media-remove', '×');
        rm.type = 'button';
        rm.title = '移除';
        rm.addEventListener('click', function () { removeAt(idx); });
        item.appendChild(rm);
        item.appendChild(el('span', 'media-name', it.file.name));
        item.appendChild(el('span', 'media-size', humanSize(it.file.size)));
        previews.appendChild(item);
      });
    }
    function removeAt(idx) {
      var it = selected[idx];
      if (it && it.url) URL.revokeObjectURL(it.url);
      selected.splice(idx, 1);
      renderPreviews();
    }
    function addFiles(files) {
      var errs = [];
      Array.prototype.slice.call(files).forEach(function (f) {
        var isImg = /^image\//.test(f.type);
        var isVid = /^video\//.test(f.type);
        if (!isImg && !isVid) { errs.push(f.name + '：不支持的格式'); return; }
        if (isImg) {
          if (f.size > MAX_IMG) { errs.push(f.name + '：图片不能超过 10MB'); return; }
          if (countType('image') >= maxImages) { errs.push('图片最多 ' + maxImages + ' 张'); return; }
        } else {
          if (f.size > MAX_VID) { errs.push(f.name + '：视频不能超过 70MB'); return; }
          if (countType('video') >= maxVideos) { errs.push('视频最多 ' + maxVideos + ' 个'); return; }
        }
        selected.push({ file: f, url: isImg ? URL.createObjectURL(f) : '', type: isImg ? 'image' : 'video' });
      });
      renderPreviews();
      if (errs.length) showErr(errs.join('；'));
    }

    if (dropZone && input) {
      dropZone.addEventListener('click', function () { input.click(); });
      dropZone.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); input.click(); }
      });
      input.addEventListener('change', function () { addFiles(input.files); input.value = ''; });
      ['dragenter', 'dragover'].forEach(function (t) {
        dropZone.addEventListener(t, function (e) {
          e.preventDefault(); e.stopPropagation(); dropZone.classList.add('dragover');
        });
      });
      ['dragleave', 'drop'].forEach(function (t) {
        dropZone.addEventListener(t, function (e) {
          e.preventDefault(); e.stopPropagation(); dropZone.classList.remove('dragover');
        });
      });
      dropZone.addEventListener('drop', function (e) {
        if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length) addFiles(e.dataTransfer.files);
      });
      // 拖到页面其他位置时阻止浏览器直接打开文件
      ['dragover', 'drop'].forEach(function (t) {
        document.addEventListener(t, function (e) { if (!dropZone.contains(e.target)) e.preventDefault(); });
      });
    }

    /* ---- 验证码弹窗 ---- */
    var modal = document.getElementById('captcha-modal');
    var codeInput = document.getElementById('captcha-code');
    var captchaBox = document.getElementById('captcha-box');
    var captchaErr = document.getElementById('captcha-err');
    var confirmBtn = document.getElementById('captcha-confirm');
    var cancelBtn = document.getElementById('captcha-cancel');
    var submitBtn = document.getElementById('compose-submit');

    function openCaptcha() {
      if (!modal) return;
      if (captchaErr) captchaErr.hidden = true;
      if (codeInput) codeInput.value = '';
      loadCaptcha();
      modal.hidden = false;
      if (codeInput) setTimeout(function () { codeInput.focus(); }, 50);
    }
    function closeCaptcha() { if (modal) modal.hidden = true; }
    function resetBtns() {
      if (confirmBtn) { confirmBtn.disabled = false; confirmBtn.textContent = '确认发布'; }
      if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = '发布'; }
      var bar = document.getElementById('upload-progress');
      if (bar) {
        bar.hidden = true;
        var span = bar.firstElementChild;
        if (span) span.style.width = '0%';
      }
    }

    if (captchaBox) captchaBox.addEventListener('click', loadCaptcha);
    if (cancelBtn) cancelBtn.addEventListener('click', closeCaptcha);
    if (modal) {
      var mask = modal.querySelector('.modal-mask');
      if (mask) mask.addEventListener('click', closeCaptcha);
    }
    if (codeInput) {
      codeInput.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') { e.preventDefault(); if (confirmBtn) confirmBtn.click(); }
      });
    }
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && modal && !modal.hidden) closeCaptcha();
    });

    /* ---- 管理员身份验证弹窗 ---- */
    var avModal = document.getElementById('admin-verify-modal');
    var avNick = document.getElementById('admin-verify-nick');
    var avUser = document.getElementById('admin-user');
    var avPass = document.getElementById('admin-pass');
    var avErr = document.getElementById('admin-verify-err');
    var avOk = document.getElementById('admin-verify-ok');
    var avCancel = document.getElementById('admin-verify-cancel');

    function closeAdminVerify() { if (avModal) avModal.hidden = true; }

    function openAdminVerify(nick) {
      if (!avModal) return;
      if (avNick) avNick.textContent = nick;
      if (avErr) avErr.hidden = true;
      if (avUser) avUser.value = '';
      if (avPass) avPass.value = '';
      avModal.hidden = false;
      if (avUser) setTimeout(function () { avUser.focus(); }, 50);
    }

    if (avCancel) avCancel.addEventListener('click', closeAdminVerify);
    if (avModal) {
      var avMask = avModal.querySelector('.modal-mask');
      if (avMask) avMask.addEventListener('click', closeAdminVerify);
    }
    if (avPass) {
      avPass.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') { e.preventDefault(); if (avOk) avOk.click(); }
      });
    }
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && avModal && !avModal.hidden) closeAdminVerify();
    });
    if (avOk) {
      avOk.addEventListener('click', function () {
        var u = (avUser ? avUser.value : '').trim();
        var p = avPass ? avPass.value : '';
        if (!u || !p) {
          if (avErr) { avErr.textContent = '请输入后台账号与密码'; avErr.hidden = false; }
          return;
        }
        adminAuth = { user: u, pass: p };
        closeAdminVerify();
        openCaptcha(); // 验证通过后继续走验证码流程
      });
    }

    // 验证码通过后真正提交（XHR 带上传进度 + AI 预审动画）
    function doSubmit(code) {
      if (captchaErr) captchaErr.hidden = true;
      if (errBox) errBox.hidden = true;
      if (confirmBtn) { confirmBtn.disabled = true; confirmBtn.textContent = '发布中...'; }
      if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = '发布中...'; }

      var fd = new FormData(form);
      // 按类型分配到服务端约定的 images / videos 字段
      selected.forEach(function (it) {
        fd.append(it.type === 'video' ? 'videos' : 'images', it.file, it.file.name);
      });
      fd.set('captcha_id', captchaBox ? (captchaBox.dataset.id || '') : '');
      fd.set('captcha_code', code);
      fd.set('fingerprint', fingerprint());
      // 管理员专享昵称：后台账号凭证（服务端强制校验）
      fd.set('admin_user', adminAuth.user || '');
      fd.set('admin_pass', adminAuth.pass || '');

      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/compose');
      xhr.setRequestHeader('X-CSRF-Token', CSRF);
      xhr.setRequestHeader('X-Requested-With', 'XMLHttpRequest');
      xhr.upload.onprogress = function (e) {
        if (!e.lengthComputable) return;
        var pct = Math.round(e.loaded / e.total * 100);
        var bar = document.getElementById('upload-progress');
        if (bar) {
          bar.hidden = false;
          var span = bar.firstElementChild;
          if (span) span.style.width = pct + '%';
        }
      };
      // 媒体上传完成、等待服务端（含 AI 预审）响应期间展示审核动画
      xhr.upload.onload = function () { closeCaptcha(); if (aiScan) aiScan.show(); };
      xhr.onload = function () {
        if (aiScan) aiScan.hide();
        resetBtns();
        var r = {};
        try { r = JSON.parse(xhr.responseText); } catch (e) { }
        if (xhr.status === 200 && r.ok) {
          window.location.href = r.redirect || '/';
          return;
        }
        closeCaptcha();
        showErr(r.msg || '发布失败，请稍后重试');
      };
      xhr.onerror = function () {
        if (aiScan) aiScan.hide();
        resetBtns();
        closeCaptcha();
        showErr('网络异常，请稍后重试');
      };
      xhr.send(fd);
    }

    // 点击「发布」：基础校验 → （管理员专享昵称时）验证后台账号 → 验证码
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      if (errBox) errBox.hidden = true;
      var content = form.querySelector('[name=content]');
      if (content && !content.value.trim()) { showErr('请填写内容'); return; }
      adminAuth = { user: '', pass: '' };
      var nickInput = form.querySelector('[name=nickname]');
      var nick = nickInput ? nickInput.value : '';
      if (isReservedNick(nick)) {
        openAdminVerify(String(nick).trim() || '匿名');
        return;
      }
      openCaptcha();
    });

    if (confirmBtn) {
      confirmBtn.addEventListener('click', function () {
        var code = (codeInput ? codeInput.value : '').trim();
        if (!/^\d{4}$/.test(code)) {
          if (captchaErr) { captchaErr.textContent = '请输入 4 位数字验证码'; captchaErr.hidden = false; }
          return;
        }
        doSubmit(code);
      });
    }
  }

  /* ---------------- 主页名片编辑 ---------------- */

  function initCardEditor() {
    var openBtn = document.getElementById('card-edit');
    var modal = document.getElementById('card-modal');
    if (!openBtn || !modal) return;
    var card = document.getElementById('profile-card');
    var titleEl = document.getElementById('profile-title');
    var bioEl = document.getElementById('profile-bio');
    var titleInput = document.getElementById('card-title-input');
    var bioInput = document.getElementById('card-bio-input');
    var styles = document.getElementById('card-styles');
    var status = document.getElementById('card-status');
    var closeBtn = document.getElementById('card-close');
    var timer = null;
    var coverEl = card ? card.querySelector('.profile-cover') : null;
    var coverPreview = document.getElementById('card-cover-preview');
    var coverEmpty = document.getElementById('card-cover-empty');
    var coverPick = document.getElementById('card-cover-pick');
    var coverInput = document.getElementById('card-cover-input');
    var coverRemove = document.getElementById('card-cover-remove');

    function setCover(url) {
      if (coverEl) coverEl.style.backgroundImage = url ? "url('" + url + "')" : '';
      if (coverPreview) coverPreview.style.backgroundImage = url ? "url('" + url + "')" : '';
      if (coverEmpty) coverEmpty.hidden = !!url;
      if (coverRemove) coverRemove.hidden = !url;
    }

    if (coverPick && coverInput) {
      coverPick.addEventListener('click', function () { coverInput.click(); });
      coverInput.addEventListener('change', function () {
        var file = coverInput.files && coverInput.files[0];
        if (!file) return;
        if (!/^image\/(jpeg|png|gif|webp)$/.test(file.type || '')) {
          toast('请选择 JPG / PNG / GIF / WebP 图片');
          coverInput.value = '';
          return;
        }
        if (file.size > 10 * 1024 * 1024) {
          toast('封面图片不能超过 10MB');
          coverInput.value = '';
          return;
        }
        var fd = new FormData();
        fd.append('cover', file, file.name || 'cover.jpg');
        coverPick.disabled = true;
        api('/api/card/cover', { method: 'POST', body: fd }).then(function (r) {
          coverPick.disabled = false;
          coverInput.value = '';
          if (!r || !r.ok) {
            toast((r && r.msg) || '封面上传失败');
            return;
          }
          setCover(r.cover);
          toast('封面已更新');
        }).catch(function () {
          coverPick.disabled = false;
          coverInput.value = '';
          toast('网络异常，请稍后重试');
        });
      });
    }
    if (coverRemove) {
      coverRemove.addEventListener('click', function () {
        coverRemove.disabled = true;
        api('/api/card/cover/remove', { method: 'POST' }).then(function (r) {
          coverRemove.disabled = false;
          if (!r || !r.ok) {
            toast((r && r.msg) || '移除失败');
            return;
          }
          setCover('');
          toast('封面已移除');
        }).catch(function () {
          coverRemove.disabled = false;
          toast('网络异常，请稍后重试');
        });
      });
    }

    function close() {
      modal.hidden = true;
      document.body.classList.remove('modal-open');
    }
    function open() {
      if (status) status.textContent = '修改后立即生效，自动保存';
      modal.hidden = false;
      document.body.classList.add('modal-open');
    }
    function selectedStyle() {
      var b = styles ? styles.querySelector('button.active') : null;
      return b ? b.getAttribute('data-style') : 'aurora';
    }

    function applyLive() {
      var title = titleInput ? titleInput.value : '';
      var bio = bioInput ? bioInput.value : '';
      if (card) card.setAttribute('data-style', selectedStyle());
      if (titleEl) {
        titleEl.textContent = title;
        titleEl.hidden = !title.trim();
      }
      if (bioEl) {
        bioEl.textContent = bio;
        bioEl.hidden = !bio.trim();
      }
    }

    // 输入即预览；停手 400ms 自动保存，无需手动确认
    function persist() {
      clearTimeout(timer);
      timer = setTimeout(function () {
        if (status) status.textContent = '保存中…';
        api('/api/card', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            title: titleInput ? titleInput.value.trim() : '',
            bio: bioInput ? bioInput.value.trim() : '',
            style: selectedStyle()
          })
        }).then(function (r) {
          if (!status) return;
          status.textContent = (r && r.ok) ? '已保存' : ((r && r.msg) || '保存失败');
        }).catch(function () {
          if (status) status.textContent = '保存失败';
        });
      }, 400);
    }

    openBtn.addEventListener('click', open);
    if (closeBtn) closeBtn.addEventListener('click', close);
    modal.addEventListener('click', function (ev) {
      if (ev.target === modal || ev.target.hasAttribute('data-card-close')) close();
    });
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape' && !modal.hidden) close();
    });
    if (styles) {
      styles.addEventListener('click', function (ev) {
        var b = ev.target.closest('button[data-style]');
        if (!b) return;
        var btns = styles.querySelectorAll('button');
        for (var i = 0; i < btns.length; i++) btns[i].classList.remove('active');
        b.classList.add('active');
        applyLive();
        persist();
      });
    }
    if (titleInput) {
      titleInput.addEventListener('input', function () {
        applyLive();
        persist();
      });
    }
    if (bioInput) {
      bioInput.addEventListener('input', function () {
        applyLive();
        persist();
      });
    }
  }

  function initFoodImagePreview() {
    var input = document.getElementById('food-image-input');
    var wrap = document.getElementById('food-image-preview');
    var img = document.getElementById('food-image-preview-img');
    if (!input || !wrap || !img) return;
    var lastURL = null;
    input.addEventListener('change', function () {
      var file = input.files && input.files[0];
      if (lastURL) { URL.revokeObjectURL(lastURL); lastURL = null; }
      if (!file) {
        wrap.hidden = true;
        img.removeAttribute('src');
        return;
      }
      lastURL = URL.createObjectURL(file);
      img.src = lastURL;
      wrap.hidden = false;
    });
  }

  /* ---------------- 必吃榜：验证码 / 星级评分 ---------------- */

  function initFoodCaptcha() {
    var box = document.getElementById('food-captcha-box');
    if (!box) return;
    var idInput = document.getElementById('food-captcha-id');
    function load() {
      api('/api/captcha').then(function (r) {
        if (!r || !r.id) return;
        box.innerHTML = r.svg;
        if (idInput) idInput.value = r.id;
      });
    }
    box.addEventListener('click', load);
    load();
  }

  function initFoodRating() {
    var wrap = document.querySelector('.star-input[data-place]');
    if (!wrap) return;
    var place = wrap.getAttribute('data-place');
    var buttons = wrap.querySelectorAll('button[data-score]');
    var status = document.getElementById('food-rate-status');
    var avgVal = document.getElementById('food-avg-value');
    var avgFill = document.getElementById('food-avg-fill');
    var avgCount = document.getElementById('food-avg-count');
    var avgStars = document.getElementById('food-avg-stars');
    var my = parseInt(wrap.getAttribute('data-my'), 10) || 0;

    function mark(n) {
      for (var i = 0; i < buttons.length; i++) {
        var s = parseInt(buttons[i].getAttribute('data-score'), 10);
        buttons[i].classList.toggle('active', s <= n);
      }
    }
    mark(my);

    for (var i = 0; i < buttons.length; i++) {
      buttons[i].addEventListener('click', function () {
        var score = parseInt(this.getAttribute('data-score'), 10);
        api('/api/food/' + place + '/rate', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ score: score })
        }).then(function (r) {
          if (!r || !r.ok) {
            toast((r && r.msg) || '评分失败');
            return;
          }
          my = score;
          mark(my);
          var avg = Number(r.avg) || 0;
          if (avgVal) avgVal.textContent = avg.toFixed(1);
          if (avgCount) avgCount.textContent = String(r.count || 0);
          if (avgFill) avgFill.style.width = Math.max(0, Math.min(100, avg / 5 * 100)) + '%';
          if (avgStars) avgStars.setAttribute('aria-label', avg.toFixed(1) + ' 分');
          if (status) status.textContent = '你的评分：' + my + ' 星';
        }).catch(function () { toast('网络异常，请稍后重试'); });
      });
    }
  }

  /* 同步主题属性：auto 跟随系统时也通知玻璃主题插件（data-clv-cz-mode） */
  function syncThemeAttrs() {
    var root = document.documentElement;
    var t = root.getAttribute('data-clv-theme') || 'dark';
    var dark = t === 'dark' || (t === 'auto' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
    root.setAttribute('data-clv-cz-mode', dark ? 'dark' : 'light');
  }

  /* ---------------- 个性化设置（服务端同步） ---------------- */

  function initPrefs() {
    var openBtn = document.getElementById('prefs-open');
    var modal = document.getElementById('prefs-modal');
    if (!openBtn || !modal) return;
    var root = document.documentElement;
    var closeBtn = document.getElementById('prefs-close');
    var font = document.getElementById('prefs-font');
    var fontVal = document.getElementById('prefs-font-value');
    var status = document.getElementById('prefs-status');
    var timer = null, fontRaf = 0, pendingSize = 0;

    function curTheme() { return root.getAttribute('data-clv-theme') || 'dark'; }
    function curCols() { return root.getAttribute('data-clv-cols') || '1'; }
    function curFont() {
      if (font && font.value) return parseInt(font.value, 10) || 100;
      var z = getComputedStyle(root).getPropertyValue('--clv-font-scale').trim();
      return parseInt(z, 10) || 100;
    }
    function setStatus(t) { if (status) status.textContent = t; }

    function mark() {
      var groups = modal.querySelectorAll('.prefs-options');
      for (var i = 0; i < groups.length; i++) {
        var g = groups[i];
        var name = g.getAttribute('data-group');
        var want = name === 'theme' ? curTheme() : curCols();
        var btns = g.querySelectorAll('button');
        for (var j = 0; j < btns.length; j++) {
          btns[j].classList.toggle('active', btns[j].getAttribute('data-value') === want);
        }
      }
      if (font) font.value = String(curFont());
      if (fontVal) fontVal.textContent = curFont() + '%';
    }

    function apply(theme, cols, size) {
      root.setAttribute('data-clv-theme', theme);
      root.setAttribute('data-clv-cols', cols);
      root.style.setProperty('--clv-font-scale', size + '%');
      if (fontVal) fontVal.textContent = size + '%';
      syncThemeAttrs();
    }

    // 改动立即生效；300ms 防抖后自动写入服务端，无需确认
    function persist() {
      clearTimeout(timer);
      timer = setTimeout(function () {
        setStatus('保存中…');
        api('/api/prefs', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            theme: curTheme(),
            columns: parseInt(curCols(), 10),
            font_scale: curFont()
          })
        }).then(function (r) {
          if (r && r.ok) setStatus('已保存');
          else setStatus((r && r.msg) || '保存失败');
        }).catch(function () { setStatus('保存失败'); });
      }, 300);
    }

    function open() {
      mark();
      setStatus('设置会自动保存');
      modal.hidden = false;
      document.body.classList.add('modal-open');
    }
    function close() {
      modal.hidden = true;
      document.body.classList.remove('modal-open');
    }

    openBtn.addEventListener('click', open);
    // 账号资料入口：关闭设置面板后再打开名片/头像流程
    var accountCard = document.getElementById('card-edit');
    var accountAvatar = document.getElementById('avatar-pick');
    if (accountCard) accountCard.addEventListener('click', close);
    if (accountAvatar) accountAvatar.addEventListener('click', close);
    if (closeBtn) closeBtn.addEventListener('click', close);
    modal.addEventListener('click', function (ev) {
      if (ev.target === modal || ev.target.hasAttribute('data-prefs-close')) close();
    });
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape' && !modal.hidden) close();
    });

    modal.addEventListener('click', function (ev) {
      var b = ev.target.closest('.prefs-options button');
      if (!b) return;
      var parent = b.parentNode;
      var btns = parent.querySelectorAll('button');
      for (var i = 0; i < btns.length; i++) btns[i].classList.remove('active');
      b.classList.add('active');
      var group = parent.getAttribute('data-group');
      if (group === 'theme') apply(b.getAttribute('data-value'), curCols(), curFont());
      else apply(curTheme(), b.getAttribute('data-value'), curFont());
      persist();
    });

    if (font) {
      font.addEventListener('input', function () {
        var size = parseInt(font.value, 10) || 100;
        pendingSize = size;
        if (fontVal) fontVal.textContent = size + '%';
        // 用 rAF 合并高频 input，缩放只跟一帧走，拖动更顺滑
        if (!fontRaf) {
          fontRaf = requestAnimationFrame(function () {
            fontRaf = 0;
            root.style.setProperty('--clv-font-scale', pendingSize + '%');
          });
        }
        persist();
      });
      font.addEventListener('change', function () {
        var size = parseInt(font.value, 10) || 100;
        root.style.setProperty('--clv-font-scale', size + '%');
        if (fontVal) fontVal.textContent = size + '%';
        persist();
      });
    }
  }

  /* ---------------- 头像上传（Canvas 裁剪） ---------------- */

  function initAvatarEditor() {
    var pick = document.getElementById('avatar-pick');
    var input = document.getElementById('avatar-input');
    var modal = document.getElementById('avatar-modal');
    var stage = document.getElementById('avatar-stage');
    var canvas = document.getElementById('avatar-canvas');
    if (!pick || !input || !modal || !stage || !canvas) return;

    var ctx = canvas.getContext('2d');
    var VIEW = canvas.width;   // 画布内部尺寸（640，CSS 显示为 320，保证清晰）
    var OUTPUT = 256;          // 输出尺寸，与后端 avatarSize 保持一致
    var zoom = document.getElementById('avatar-zoom');
    var saveBtn = document.getElementById('avatar-save');
    var cancelBtn = document.getElementById('avatar-cancel');

    var img = null;
    var scale = 1, minScale = 1, ox = 0, oy = 0;
    var dragging = false, startX = 0, startY = 0, startOx = 0, startOy = 0;
    var objURL = '';

    function clamp() {
      var w = img.naturalWidth * scale, h = img.naturalHeight * scale;
      ox = w <= VIEW ? (VIEW - w) / 2 : Math.min(0, Math.max(VIEW - w, ox));
      oy = h <= VIEW ? (VIEW - h) / 2 : Math.min(0, Math.max(VIEW - h, oy));
    }

    function draw() {
      if (!img) return;
      ctx.imageSmoothingEnabled = true;
      ctx.imageSmoothingQuality = 'high';
      ctx.clearRect(0, 0, VIEW, VIEW);
      // 透明 PNG 统一铺白底，避免输出 JPEG 后透明区域变黑
      ctx.fillStyle = '#fff';
      ctx.fillRect(0, 0, VIEW, VIEW);
      ctx.drawImage(img, ox, oy, img.naturalWidth * scale, img.naturalHeight * scale);
    }

    // 以 (ax, ay) 为锚点缩放，保证手指/鼠标下的位置不跳
    function setScale(next, ax, ay) {
      if (!img) return;
      next = Math.min(minScale * 3, Math.max(minScale, next));
      var k = next / scale;
      ox = ax - (ax - ox) * k;
      oy = ay - (ay - oy) * k;
      scale = next;
      clamp();
      draw();
      if (zoom) zoom.value = String(Math.round((scale / minScale - 1) * 50));
    }

    function open(file) {
      if (!/^image\/(jpeg|png|gif|webp)$/.test(file.type || '')) {
        toast('请选择 JPG / PNG / GIF / WebP 图片');
        input.value = '';
        return;
      }
      if (file.size > 10 * 1024 * 1024) {
        toast('头像图片不能超过 10MB');
        input.value = '';
        return;
      }
      if (objURL) URL.revokeObjectURL(objURL);
      objURL = URL.createObjectURL(file);
      var im = new Image();
      im.onload = function () {
        img = im;
        minScale = Math.max(VIEW / im.naturalWidth, VIEW / im.naturalHeight);
        scale = minScale;
        ox = (VIEW - im.naturalWidth * scale) / 2;
        oy = (VIEW - im.naturalHeight * scale) / 2;
        if (zoom) zoom.value = '0';
        draw();
        modal.hidden = false;
        document.body.classList.add('modal-open');
        saveBtn.focus();
      };
      im.onerror = function () { toast('图片读取失败，请换一张试试'); input.value = ''; };
      im.src = objURL;
    }

    function close() {
      modal.hidden = true;
      document.body.classList.remove('modal-open');
      input.value = '';
      img = null;
      if (objURL) { URL.revokeObjectURL(objURL); objURL = ''; }
    }

    // 上传成功后同步页面上的头像（顶栏、个人主页、本人帖子卡片）
    function applyAvatar(url) {
      var cur = document.getElementById('avatar-current');
      if (cur) {
        if (cur.tagName === 'IMG') {
          cur.src = url;
        } else {
          var big = el('img', 'avatar big');
          big.id = 'avatar-current';
          big.src = url;
          big.alt = cur.textContent || '';
          cur.parentNode.replaceChild(big, cur);
        }
      }
      var link = document.querySelector('.topbar .avatar-link') || document.querySelector('.avatar-link');
      if (link) {
        var old = link.querySelector('.avatar');
        var alt = old ? (old.getAttribute('alt') || old.textContent || '') : '';
        var top = el('img', 'avatar');
        top.src = url;
        top.alt = alt;
        link.innerHTML = '';
        link.appendChild(top);
      }
      var cards = document.querySelectorAll('.masonry .post-card .card-head .avatar');
      for (var i = 0; i < cards.length; i++) {
        var node = cards[i];
        if (node.tagName === 'IMG') { node.src = url; continue; }
        var ni = el('img', 'avatar');
        ni.src = url;
        ni.alt = node.textContent || '';
        node.parentNode.replaceChild(ni, node);
      }
      var meta = document.querySelector('meta[name="clv-user-avatar"]');
      if (meta) meta.setAttribute('content', url);
    }

    pick.addEventListener('click', function () { input.click(); });
    var quick = document.getElementById('avatar-quick');
    if (quick) quick.addEventListener('click', function () { input.click(); });
    input.addEventListener('change', function () {
      if (input.files && input.files[0]) open(input.files[0]);
    });
    if (cancelBtn) cancelBtn.addEventListener('click', close);
    modal.addEventListener('click', function (ev) { if (ev.target === modal) close(); });
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape' && !modal.hidden) close();
    });

    // 拖拽移动
    stage.addEventListener('pointerdown', function (ev) {
      if (!img) return;
      dragging = true;
      stage.classList.add('dragging');
      startX = ev.clientX; startY = ev.clientY;
      startOx = ox; startOy = oy;
      if (stage.setPointerCapture) {
        try { stage.setPointerCapture(ev.pointerId); } catch (e) { }
      }
      ev.preventDefault();
    });
    stage.addEventListener('pointermove', function (ev) {
      if (!dragging || !img) return;
      var rect = stage.getBoundingClientRect();
      var k = VIEW / Math.max(1, rect.width);
      ox = startOx + (ev.clientX - startX) * k;
      oy = startOy + (ev.clientY - startY) * k;
      clamp();
      draw();
    });
    function endDrag() {
      if (!dragging) return;
      dragging = false;
      stage.classList.remove('dragging');
    }
    stage.addEventListener('pointerup', endDrag);
    stage.addEventListener('pointercancel', endDrag);
    stage.addEventListener('lostpointercapture', endDrag);

    // 滚轮缩放
    stage.addEventListener('wheel', function (ev) {
      if (!img) return;
      ev.preventDefault();
      var rect = stage.getBoundingClientRect();
      var ax = (ev.clientX - rect.left) * (VIEW / rect.width);
      var ay = (ev.clientY - rect.top) * (VIEW / rect.height);
      setScale(scale * Math.exp(-ev.deltaY * 0.0015), ax, ay);
    }, { passive: false });

    if (zoom) {
      zoom.addEventListener('input', function () {
        if (!img) return;
        var v = parseInt(zoom.value, 10) || 0;
        setScale(minScale * (1 + v / 50), VIEW / 2, VIEW / 2);
      });
    }

    // 双击复位
    stage.addEventListener('dblclick', function () {
      if (!img) return;
      scale = minScale;
      ox = (VIEW - img.naturalWidth * scale) / 2;
      oy = (VIEW - img.naturalHeight * scale) / 2;
      if (zoom) zoom.value = '0';
      draw();
    });

    saveBtn.addEventListener('click', function () {
      if (!img) return;
      var out = document.createElement('canvas');
      out.width = out.height = OUTPUT;
      out.getContext('2d').drawImage(canvas, 0, 0, VIEW, VIEW, 0, 0, OUTPUT, OUTPUT);
      saveBtn.disabled = true;
      out.toBlob(function (blob) {
        if (!blob) {
          saveBtn.disabled = false;
          toast('裁剪失败，请重试');
          return;
        }
        var fd = new FormData();
        fd.append('avatar', blob, 'avatar.jpg');
        api('/profile/avatar', { method: 'POST', body: fd }).then(function (r) {
          saveBtn.disabled = false;
          if (!r || !r.ok) {
            toast((r && r.msg) || '上传失败，请稍后重试');
            return;
          }
          applyAvatar(r.avatar);
          close();
          toast('头像已更新');
        }).catch(function () {
          saveBtn.disabled = false;
          toast('网络异常，请稍后重试');
        });
      }, 'image/jpeg', 0.92);
    });
  }

  /* ---------------- 注册验证码 ---------------- */

  function initSendCode() {
    var btn = document.getElementById('send-code');
    if (!btn) return;
    btn.addEventListener('click', function () {
      var emailInput = document.querySelector('input[name=' + btn.dataset.email + ']');
      if (!emailInput || !emailInput.value) { toast('请先填写邮箱'); return; }
      var fd = new FormData();
      fd.append('email', emailInput.value);
      btn.disabled = true;
      api('/api/register-code', { method: 'POST', body: fd }).then(function (r) {
        if (!r || !r.ok) {
          toast((r && r.msg) || '发送失败');
          btn.disabled = false;
          return;
        }
        var left = 60;
        btn.textContent = left + ' 秒后重发';
        var timer = setInterval(function () {
          left--;
          btn.textContent = left + ' 秒后重发';
          if (left <= 0) {
            clearInterval(timer);
            btn.disabled = false;
            btn.textContent = '发送验证码';
          }
        }, 1000);
      });
    });
  }

  /* ---------------- 安装向导 ---------------- */

  function initInstall() {
    var form = document.getElementById('install-form');
    if (!form) return;
    var step = 1;
    var dots = form.querySelectorAll('.step-dot');
    var sections = form.querySelectorAll('.install-step');
    var prev = document.getElementById('install-prev');
    var next = document.getElementById('install-next');
    var submit = document.getElementById('install-submit');
    var errBox = document.getElementById('install-err');

    // MySQL 字段显隐
    var mysqlFields = document.getElementById('mysql-fields');
    form.querySelectorAll('input[name=db_type]').forEach(function (radio) {
      radio.addEventListener('change', function () {
        mysqlFields.hidden = form.querySelector('input[name=db_type]:checked').value !== 'mysql';
      });
    });

    // 分步模式：JS 可用时隐藏非当前步骤；提交按钮始终可见（便于无 JS 降级），
    // 前两步降为次级样式，把「下一步」作为主操作
    function render() {
      sections.forEach(function (s) { s.hidden = Number(s.dataset.step) !== step; });
      dots.forEach(function (d) { d.classList.toggle('active', Number(d.dataset.step) <= step); });
      prev.hidden = step === 1;
      next.hidden = step === 3;
      submit.classList.toggle('btn-primary', step === 3);
      submit.classList.toggle('btn-ghost', step !== 3);
      errBox.hidden = true;
    }

    function validate(s) {
      if (s === 1 && form.querySelector('input[name=db_type]:checked').value === 'mysql') {
        var need = ['mysql_host', 'mysql_port', 'mysql_name', 'mysql_user'];
        for (var i = 0; i < need.length; i++) {
          if (!form.querySelector('[name=' + need[i] + ']').value.trim()) {
            errBox.textContent = '请完整填写 MySQL 连接信息';
            errBox.hidden = false;
            return false;
          }
        }
      }
      if (s === 2 && !form.querySelector('[name=site_name]').value.trim()) {
        errBox.textContent = '请填写网站名称';
        errBox.hidden = false;
        return false;
      }
      return true;
    }

    next.addEventListener('click', function () {
      if (!validate(step)) return;
      step = Math.min(3, step + 1);
      render();
    });
    prev.addEventListener('click', function () {
      step = Math.max(1, step - 1);
      render();
    });

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      // 提交前定位到首个校验失败的步骤
      if (!validate(1)) { step = 1; render(); return; }
      if (!validate(2)) { step = 2; render(); return; }
      step = 3;
      render();
      var user = form.querySelector('[name=admin_user]').value.trim();
      var pass = form.querySelector('[name=admin_pass]').value;
      if (user.length < 3 || pass.length < 6) {
        errBox.textContent = '管理员用户名至少 3 位、密码至少 6 位';
        errBox.hidden = false;
        return;
      }
      submit.disabled = true;
      submit.textContent = '安装中...';
      fetch('/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest', 'X-CSRF-Token': CSRF },
        body: new URLSearchParams(new FormData(form)).toString()
      }).then(function (r) { return r.json(); }).then(function (r) {
        if (r && r.ok) { window.location.href = r.redirect || '/'; return; }
        errBox.textContent = (r && r.msg) || '安装失败，请检查填写内容';
        errBox.hidden = false;
        submit.disabled = false;
        submit.textContent = '完成安装';
      }).catch(function () {
        errBox.textContent = '网络异常，请稍后重试';
        errBox.hidden = false;
        submit.disabled = false;
        submit.textContent = '完成安装';
      });
    });
    render();
  }

  /* ---------------- AI 巡查进度动画（后台举报页） ---------------- */

  function initPatrol() {
    var overlay = document.getElementById('patrol-overlay');
    if (!overlay) return;
    var btn = document.getElementById('patrol-btn');
    var ring = document.getElementById('patrol-ring');
    var CIRC = 326.7; // 2πr, r=52
    var timer = null;

    function txt(id, v) { var e = document.getElementById(id); if (e) e.textContent = v; }

    function render(p, percent) {
      if (ring) ring.style.strokeDashoffset = String(CIRC * (1 - (percent || 0) / 100));
      txt('patrol-pct', percent || 0);
      txt('patrol-total', p.total || 0);
      txt('patrol-done', p.done || 0);
      txt('patrol-keep', p.kept || 0);
      txt('patrol-hide', p.hidden || 0);
      txt('patrol-del', p.deleted || 0);
      var bar = document.getElementById('patrol-bar');
      if (bar) bar.style.width = (percent || 0) + '%';
      var sub = document.getElementById('patrol-sub');
      if (sub) {
        sub.textContent = p.stage || '正在准备';
        if (p.running) {
          var d = el('span', 'dots');
          d.appendChild(el('i')); d.appendChild(el('i')); d.appendChild(el('i'));
          sub.appendChild(d);
        }
      }
      if (p.current && p.running) {
        var cur = document.getElementById('patrol-sub');
        if (cur) cur.title = '当前检查：' + p.current;
      }
    }

    function finish(p) {
      var card = document.getElementById('patrol-card');
      if (card) card.classList.add('scan-done');
      txt('patrol-title', '巡查完成');
      var sub = document.getElementById('patrol-sub');
      if (sub) sub.textContent = p.message || '本轮巡查已结束';
      var fin = document.getElementById('patrol-finish');
      if (fin) fin.hidden = false;
      if (timer) { clearInterval(timer); timer = null; }
      var hb = document.getElementById('patrol-hide-btn');
      if (hb) hb.hidden = true;
    }

    function poll() {
      api('/admin/reports/patrol/status').then(function (r) {
        if (!r || !r.ok || !r.progress) return;
        var p = r.progress;
        render(p, r.percent);
        if (!p.running) finish(p);
      });
    }

    function startPolling() {
      poll();
      if (timer) clearInterval(timer);
      timer = setInterval(poll, 1000);
    }

    function reset() {
      var card = document.getElementById('patrol-card');
      if (card) card.classList.remove('scan-done');
      txt('patrol-title', 'AI 智能巡查中');
      var hb = document.getElementById('patrol-hide-btn');
      if (hb) hb.hidden = false;
      var fin = document.getElementById('patrol-finish');
      if (fin) fin.hidden = true;
      render({ total: 0, done: 0, running: true, stage: '正在启动巡查' }, 0);
    }

    // 巡查入口是 POST 表单：JS 可用时拦截提交并播放进度动画，
    // JS 不可用时正常提交（服务端 303 回本页，刷新不会重复触发）
    var form = document.getElementById('patrol-form');
    if (form) {
      form.addEventListener('submit', function (ev) {
        ev.preventDefault();
        if (btn) btn.disabled = true;
        reset();
        overlay.hidden = false;
        api('/admin/reports/patrol', { method: 'POST' }).then(function (r) {
          if (btn) btn.disabled = false;
          if (!r || !r.ok) {
            overlay.hidden = true;
            toast((r && r.msg) || '巡查启动失败');
            return;
          }
          startPolling();
        });
      });
    }

    var hideBtn = document.getElementById('patrol-hide-btn');
    if (hideBtn) {
      hideBtn.addEventListener('click', function () { overlay.hidden = true; });
    }
    var finBtn = document.getElementById('patrol-finish');
    if (finBtn) {
      finBtn.addEventListener('click', function () { window.location.reload(); });
    }

    // 进入页面时若已有巡查在后台运行，自动展示进度
    if (overlay.dataset.running === '1') {
      overlay.hidden = false;
      startPolling();
    }
  }

  /* ---------------- 后台交互 ---------------- */

  window.openDonate = function () {
    var m = document.getElementById('donate-modal');
    if (m) m.hidden = false;
  };
  window.closeDonate = function () {
    var m = document.getElementById('donate-modal');
    if (m) m.hidden = true;
  };

  function initAdmin() {
    // 危险操作二次确认
    document.querySelectorAll('form[data-confirm]').forEach(function (f) {
      f.addEventListener('submit', function (ev) {
        if (!confirm(f.dataset.confirm)) ev.preventDefault();
      });
    });
    // 移动端侧边栏
    var toggle = document.getElementById('side-toggle');
    var side = document.getElementById('sidebar');
    if (toggle && side) {
      toggle.addEventListener('click', function () { side.classList.toggle('open'); });
    }
  }

  /* ---------------- 首页话题栏：话题过多时收起为单行 ---------------- */
  function initTopicBar() {
    var chips = document.getElementById('topic-chips');
    var toggle = document.getElementById('topic-toggle');
    if (!chips || !toggle) return;
    var first = chips.firstElementChild;
    if (!first) return;

    // 先按默认样式渲染，若换行超过一行说明话题较多
    var wrapped = false;
    for (var i = 1; i < chips.children.length; i++) {
      if (chips.children[i].offsetTop !== first.offsetTop) { wrapped = true; break; }
    }
    if (!wrapped) return; // 话题不多，保持原有的自动换行

    chips.classList.add('collapsed');
    toggle.hidden = false;
    var label = toggle.querySelector('span');

    // 选中的话题可能被滚出可视区，居中显示它
    function revealActive() {
      var act = chips.querySelector('.chip.active');
      if (!act) return;
      chips.scrollLeft = act.offsetLeft - (chips.clientWidth - act.offsetWidth) / 2;
    }
    revealActive();

    var rt = null;
    window.addEventListener('resize', function () {
      if (!chips.classList.contains('collapsed')) return;
      clearTimeout(rt);
      rt = setTimeout(revealActive, 150);
    });

    // PC 端：竖向滚轮转横向滚动
    chips.addEventListener('wheel', function (e) {
      if (!chips.classList.contains('collapsed')) return;
      if (Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
      e.preventDefault();
      chips.scrollLeft += e.deltaY;
    }, { passive: false });

    toggle.addEventListener('click', function () {
      if (chips.classList.contains('expanded')) {
        chips.classList.remove('expanded');
        chips.classList.add('collapsed');
        toggle.setAttribute('aria-expanded', 'false');
        if (label) label.textContent = '展开';
        chips.scrollTop = 0;
        revealActive();
      } else {
        chips.classList.remove('collapsed');
        chips.classList.add('expanded');
        toggle.setAttribute('aria-expanded', 'true');
        if (label) label.textContent = '收起';
      }
    });
  }

  /* ---------------- 旧版数据迁移（后台） ---------------- */
  function initMigrate() {
    var form = document.getElementById('migrate-form');
    if (!form) return;
    var fileInput = document.getElementById('migrate-file');
    var btn = document.getElementById('migrate-start');
    var bar = document.getElementById('migrate-progress');
    var okBox = document.getElementById('migrate-ok');
    var errBox = document.getElementById('migrate-err');
    var resultCard = document.getElementById('migrate-result');
    var resultBody = document.getElementById('migrate-result-body');
    var timer = null;

    function setBar(pct) {
      if (!bar) return;
      bar.hidden = false;
      var span = bar.firstElementChild;
      if (span) span.style.width = pct + '%';
    }
    function hideBar() { if (bar) bar.hidden = true; }
    function idleBtn() { if (btn) { btn.disabled = false; btn.textContent = '开始迁移'; } }
    function showErr(msg) {
      if (errBox) { errBox.textContent = msg; errBox.hidden = false; }
      if (okBox) okBox.hidden = true;
    }

    function renderStats(st, dry) {
      if (!resultCard || !resultBody) return;
      var rows = [
        ['数据来源', st.source === 'mysqldump' ? 'MySQL 导出文件' : 'SQLite 数据库'],
        ['帖子', st.posts], ['评论', st.comments], ['话题', st.topics],
        ['点赞补齐', st.likes], ['媒体文件', st.media],
        ['公告', st.notices], ['用户', st.users], ['管理员', st.admins],
        ['封禁名单', st.bans], ['举报', st.reports],
        ['跳过（已存在）', st.skipped], ['媒体文件缺失', st.missing], ['关联失效已忽略', st.no_post_ref]
      ];
      var html = '<table class="table"><tbody>';
      for (var i = 0; i < rows.length; i++) {
        html += '<tr><td>' + rows[i][0] + '</td><td><b>' + rows[i][1] + '</b></td></tr>';
      }
      html += '</tbody></table>';
      html += dry
        ? '<p class="form-ok">以上为试运行结果，未写入任何数据。确认无误后，取消勾选「试运行」再执行一次即可正式导入。</p>'
        : '<p class="form-ok">迁移完成，刷新前台即可看到数据。若媒体文件缺失较多，请检查旧版 uploads 目录路径是否正确。</p>';
      resultBody.innerHTML = html;
      resultCard.hidden = false;
    }

    // 轮询迁移进度（服务端异步执行，避免大文件导入时请求超时）
    function poll() {
      var xhr = new XMLHttpRequest();
      xhr.open('GET', '/admin/migrate/status');
      xhr.setRequestHeader('X-Requested-With', 'XMLHttpRequest');
      xhr.onload = function () {
        var st = {};
        try { st = JSON.parse(xhr.responseText); } catch (e) { return; }
        if (st.running) { setBar(70); return; }
        if (timer) { clearInterval(timer); timer = null; }
        hideBar();
        idleBtn();
        if (st.err) { showErr('迁移失败：' + st.err); return; }
        if (st.stats) {
          if (okBox) { okBox.textContent = st.dry_run ? '试运行完成' : '迁移执行完成'; okBox.hidden = false; }
          renderStats(st.stats, st.dry_run);
        }
      };
      xhr.send();
    }

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      if (errBox) errBox.hidden = true;
      if (okBox) okBox.hidden = true;
      if (fileInput && !fileInput.files.length) { showErr('请选择旧版数据库文件'); return; }
      if (resultCard) resultCard.hidden = true;

      var fd = new FormData(form);
      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/admin/migrate/start');
      xhr.setRequestHeader('X-CSRF-Token', CSRF);
      xhr.setRequestHeader('X-Requested-With', 'XMLHttpRequest');
      if (btn) { btn.disabled = true; btn.textContent = '上传中...'; }
      xhr.upload.onprogress = function (e) {
        if (e.lengthComputable) setBar(Math.round(e.loaded / e.total * 100));
      };
      xhr.upload.onload = function () { if (btn) btn.textContent = '迁移中...'; };
      xhr.onload = function () {
        var r = {};
        try { r = JSON.parse(xhr.responseText); } catch (e) { }
        if (xhr.status !== 200 || !r.ok) {
          hideBar();
          idleBtn();
          showErr(r.msg || '启动迁移失败');
          return;
        }
        setBar(70);
        if (timer) clearInterval(timer);
        timer = setInterval(poll, 1200);
        poll();
      };
      xhr.onerror = function () { hideBar(); idleBtn(); showErr('网络异常，请稍后重试'); };
      xhr.send(fd);
    });
  }

  /* ---------------- 插件扩展接口 ---------------- */
  // 与 head 中的预初始化对象合并，保证插件在任意位置注入的脚本都能安全注册
  var CLV = (window.CLV = window.CLV || {});
  CLV.version = ((document.querySelector('meta[name="clv-version"]') || {}).content) || '';
  CLV.csrf = CSRF;
  CLV.api = api;
  CLV.el = el;
  CLV.toast = toast;
  CLV.fingerprint = fingerprint;
  CLV.hooks = CLV.hooks || {};
  CLV.hooks.cardHtml = CLV.hooks.cardHtml || [];
  CLV.hooks.cardEl = CLV.hooks.cardEl || [];
  CLV.hooks.afterRender = CLV.hooks.afterRender || [];

  /* ---------------- 启动 ---------------- */

  document.addEventListener('DOMContentLoaded', function () {
    initSplash();
    initTip();
    initNotice();
    initMasonry();
    initCommentForm();
    initCompose();
    initSendCode();
    initInstall();
    initPatrol();
    initAdmin();
    initMigrate();
    initTopicBar();
    initAvatarEditor();
    initPrefs();
    initCardEditor();
    initFoodImagePreview();
    initFoodCaptcha();
    initFoodRating();
    syncThemeAttrs();
  });
})();
