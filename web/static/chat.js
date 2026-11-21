/* ===================================================================
   ClearLove 聊天室前端（原生 JS，无第三方依赖）
   - 登录用户共享聊天：文字 + 图片
   - 首屏由服务端渲染，脚本负责发送、增量轮询、加载更早消息
   - 昵称/头像全部来自账号，界面不提供匿名入口
   =================================================================== */
(function () {
  'use strict';

  var page = document.getElementById('chat-page');
  if (!page) return;

  var list = document.getElementById('chat-list');
  var form = document.getElementById('chat-form');
  var textarea = document.getElementById('chat-text');
  var fileInput = document.getElementById('chat-image');
  var sendBtn = document.getElementById('chat-send');
  var countEl = document.getElementById('chat-count');
  var errEl = document.getElementById('chat-err');
  var emptyEl = document.getElementById('chat-empty');
  var moreBtn = document.getElementById('chat-more');
  var jumpBtn = document.getElementById('chat-jump');
  var jumpLabel = document.getElementById('chat-jump-label');
  var preview = document.getElementById('chat-preview');
  var previewImg = document.getElementById('chat-preview-img');
  var removeBtn = document.getElementById('chat-preview-remove');
  if (!list || !form || !textarea || !sendBtn) return;

  var csrfMeta = document.querySelector('meta[name=csrf]');
  var CSRF = csrfMeta ? csrfMeta.content : '';
  var uid = String(page.getAttribute('data-uid') || '');

  var seen = Object.create(null);
  var lastId = 0;
  var oldestId = parseInt(page.getAttribute('data-oldest'), 10) || 0;
  var stick = true;
  var unread = 0;
  var sending = false;
  var pollBusy = false;
  var polling = true;
  var pendingFile = null;
  var previewURL = '';

  /* ---------------- 小工具 ---------------- */

  function pad2(n) { return n < 10 ? '0' + n : String(n); }

  function formatTime(raw) {
    if (!raw) return '';
    var d = new Date(raw);
    if (isNaN(d.getTime())) return String(raw);
    var now = new Date();
    var hm = pad2(d.getHours()) + ':' + pad2(d.getMinutes());
    var sameDay = d.getFullYear() === now.getFullYear() &&
      d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
    return sameDay ? hm : pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' ' + hm;
  }

  function firstChar(name) {
    var chars = Array.from(String(name || '用'));
    return chars[0] || '用';
  }

  function showError(msg) {
    if (!errEl) return;
    errEl.textContent = msg || '操作失败，请稍后重试';
    errEl.hidden = false;
  }

  function hideError() {
    if (!errEl) return;
    errEl.hidden = true;
    errEl.textContent = '';
  }

  function nearBottom() {
    return list.scrollHeight - list.scrollTop - list.clientHeight < 120;
  }

  function scrollToBottom(smooth) {
    if (smooth && list.scrollTo) {
      list.scrollTo({ top: list.scrollHeight, behavior: 'smooth' });
    } else {
      list.scrollTop = list.scrollHeight;
    }
  }

  function hideJump() {
    if (!jumpBtn) return;
    jumpBtn.hidden = true;
    if (jumpLabel) jumpLabel.textContent = '有新消息';
  }

  /* ---------------- 消息渲染（全部使用 textContent，杜绝 XSS） ---------------- */

  function buildMessage(m) {
    var mine = String(m.user_id) === uid;
    var nickname = m.nickname || '用户';
    var profileURL = '/u/' + encodeURIComponent(String(m.user_id));

    var article = document.createElement('article');
    article.className = 'chat-msg' + (mine ? ' mine' : '');
    article.setAttribute('data-id', String(m.id));
    article.setAttribute('data-user', String(m.user_id));

    var avatarLink = document.createElement('a');
    avatarLink.className = 'avatar-link chat-avatar';
    avatarLink.href = profileURL;
    avatarLink.title = nickname + ' 的主页';
    if (m.avatar) {
      var avatarImg = document.createElement('img');
      avatarImg.className = 'avatar';
      avatarImg.src = m.avatar;
      avatarImg.alt = nickname;
      avatarImg.loading = 'lazy';
      avatarImg.decoding = 'async';
      avatarLink.appendChild(avatarImg);
    } else {
      var avatarText = document.createElement('span');
      avatarText.className = 'avatar';
      avatarText.textContent = firstChar(nickname);
      avatarLink.appendChild(avatarText);
    }

    var body = document.createElement('div');
    body.className = 'chat-msg-body';

    var head = document.createElement('div');
    head.className = 'chat-msg-head';

    var nick = document.createElement('a');
    nick.className = 'nickname';
    nick.href = profileURL;
    nick.textContent = nickname;

    var levelNum = parseInt(m.level, 10) || 1;
    var levelBadge = document.createElement('span');
    levelBadge.className = 'level-badge lv' + levelNum;
    levelBadge.textContent = 'Lv' + levelNum;

    var time = document.createElement('time');
    time.dateTime = m.created_at || '';
    time.textContent = formatTime(m.created_at);
    var created = m.created_at ? new Date(m.created_at) : null;
    if (created && !isNaN(created.getTime())) time.title = created.toLocaleString();

    head.appendChild(nick);
    head.appendChild(levelBadge);
    head.appendChild(time);

    var bubble = document.createElement('div');
    bubble.className = 'chat-bubble' + (m.image && !m.content ? ' has-image-only' : '');
    if (m.content) {
      var text = document.createElement('p');
      text.className = 'chat-text';
      text.textContent = m.content;
      bubble.appendChild(text);
    }
    if (m.image) {
      var imageLink = document.createElement('a');
      imageLink.className = 'chat-image-link';
      imageLink.href = m.image;
      imageLink.target = '_blank';
      imageLink.rel = 'noopener';
      var image = document.createElement('img');
      image.className = 'chat-image';
      image.src = m.image;
      image.alt = nickname + ' 发送的图片';
      image.loading = 'lazy';
      image.decoding = 'async';
      imageLink.appendChild(image);
      bubble.appendChild(imageLink);
    }

    body.appendChild(head);
    body.appendChild(bubble);
    article.appendChild(avatarLink);
    article.appendChild(body);
    return article;
  }

  // 首屏消息：登记已有 ID，避免轮询重复插入
  Array.prototype.forEach.call(list.querySelectorAll('.chat-msg'), function (node) {
    var id = parseInt(node.getAttribute('data-id'), 10) || 0;
    if (!id) return;
    seen[id] = true;
    if (id > lastId) lastId = id;
    if (!oldestId || id < oldestId) oldestId = id;
  });

  function appendMessages(messages, incoming) {
    var added = 0;
    (messages || []).forEach(function (m) {
      var id = parseInt(m.id, 10);
      if (!id || seen[id]) return;
      seen[id] = true;
      list.appendChild(buildMessage(m));
      added++;
      if (id > lastId) lastId = id;
      if (!oldestId || id < oldestId) oldestId = id;
    });
    if (!added) return;
    if (emptyEl) emptyEl.hidden = true;
    if (incoming && !stick) {
      unread += added;
      if (jumpBtn) {
        if (jumpLabel) jumpLabel.textContent = unread + ' 条新消息';
        jumpBtn.hidden = false;
      }
      return;
    }
    scrollToBottom(!!incoming);
  }

  /* ---------------- 滚动行为 ---------------- */

  list.addEventListener('scroll', function () {
    stick = nearBottom();
    if (stick) {
      unread = 0;
      hideJump();
    }
  });

  // 图片加载完成后若仍停留在底部，保持贴底
  list.addEventListener('load', function (e) {
    if (e.target && e.target.tagName === 'IMG' && stick) scrollToBottom(false);
  }, true);

  if (jumpBtn) {
    jumpBtn.addEventListener('click', function () {
      stick = true;
      unread = 0;
      hideJump();
      scrollToBottom(true);
    });
  }

  requestAnimationFrame(function () { scrollToBottom(false); });
  window.addEventListener('load', function () { if (stick) scrollToBottom(false); });

  /* ---------------- 增量轮询 ---------------- */

  function fetchJSON(url) {
    return fetch(url, {
      credentials: 'same-origin',
      headers: {
        'X-Requested-With': 'XMLHttpRequest',
        'Accept': 'application/json'
      }
    }).then(function (res) {
      if (res.status === 401) {
        var err = new Error('登录状态已失效，请刷新页面后重新登录');
        err.auth = true;
        throw err;
      }
      return res.json().catch(function () {
        throw new Error('服务器返回异常（HTTP ' + res.status + '）');
      });
    });
  }

  function pollNew() {
    if (!polling || pollBusy || document.hidden) return;
    pollBusy = true;
    fetchJSON('/api/chat/messages?after=' + encodeURIComponent(lastId) + '&limit=50')
      .then(function (data) {
        if (data && data.ok) appendMessages(data.messages, true);
      })
      .catch(function (err) {
        if (err && err.auth) {
          polling = false;
          showError(err.message);
        }
        // 网络抖动不打扰用户，下一轮自动重试
      })
      .then(function () { pollBusy = false; }, function () { pollBusy = false; });
  }

  window.setInterval(pollNew, 3500);
  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) pollNew();
  });

  /* ---------------- 加载更早消息 ---------------- */

  if (moreBtn) {
    moreBtn.addEventListener('click', function () {
      if (moreBtn.disabled) return;
      if (!oldestId) {
        moreBtn.hidden = true;
        return;
      }
      moreBtn.disabled = true;
      var prevHeight = list.scrollHeight;
      var prevTop = list.scrollTop;
      fetchJSON('/api/chat/messages?before=' + encodeURIComponent(oldestId) + '&limit=30')
        .then(function (data) {
          if (!data || !data.ok) throw new Error((data && data.msg) || '加载失败，请稍后重试');
          var anchor = list.querySelector('.chat-msg');
          var added = 0;
          (data.messages || []).forEach(function (m) {
            var id = parseInt(m.id, 10);
            if (!id || seen[id]) return;
            seen[id] = true;
            var node = buildMessage(m);
            if (anchor) list.insertBefore(node, anchor); else list.appendChild(node);
            added++;
            if (!oldestId || id < oldestId) oldestId = id;
            if (id > lastId) lastId = id;
          });
          if (added) {
            if (emptyEl) emptyEl.hidden = true;
            // 保持用户当前阅读位置不跳动
            list.scrollTop = prevTop + (list.scrollHeight - prevHeight);
          }
          if (data.has_more === false) moreBtn.hidden = true;
        })
        .catch(function (err) {
          if (err && err.auth) {
            polling = false;
            showError(err.message);
          } else {
            showError((err && err.message) || '加载失败，请稍后重试');
          }
        })
        .then(function () { moreBtn.disabled = false; }, function () { moreBtn.disabled = false; });
    });
  }

  /* ---------------- 输入区：字数、自动增高、图片预览 ---------------- */

  function updateCount() {
    if (countEl) {
      countEl.textContent = textarea.value.length + '/1000';
      countEl.classList.toggle('over', textarea.value.length > 1000);
    }
  }

  function autoGrow() {
    textarea.style.height = 'auto';
    textarea.style.height = Math.max(40, Math.min(textarea.scrollHeight, 132)) + 'px';
  }

  function clearPreview() {
    if (previewURL) {
      try { URL.revokeObjectURL(previewURL); } catch (e) { /* ignore */ }
      previewURL = '';
    }
    pendingFile = null;
    if (fileInput) fileInput.value = '';
    if (preview) preview.hidden = true;
    if (previewImg) previewImg.removeAttribute('src');
  }

  function handleFile(file) {
    if (!file) {
      clearPreview();
      return;
    }
    if (!/^image\//i.test(file.type || '')) {
      clearPreview();
      showError('只能发送图片文件');
      return;
    }
    if (file.size > 10 * 1024 * 1024) {
      clearPreview();
      showError('图片不能超过 10MB');
      return;
    }
    hideError();
    pendingFile = file;
    if (previewURL) {
      try { URL.revokeObjectURL(previewURL); } catch (e) { /* ignore */ }
    }
    previewURL = URL.createObjectURL(file);
    if (previewImg) previewImg.src = previewURL;
    if (preview) preview.hidden = false;
    autoGrow();
  }

  if (fileInput) {
    fileInput.addEventListener('change', function () {
      handleFile(fileInput.files && fileInput.files[0]);
    });
  }
  if (removeBtn) {
    removeBtn.addEventListener('click', function () {
      clearPreview();
      hideError();
    });
  }

  // 直接粘贴截图/图片
  form.addEventListener('paste', function (e) {
    var items = e.clipboardData && e.clipboardData.items;
    if (!items) return;
    for (var i = 0; i < items.length; i++) {
      if (items[i].type && items[i].type.indexOf('image/') === 0) {
        var file = items[i].getAsFile();
        if (!file) continue;
        e.preventDefault();
        handleFile(file);
        return;
      }
    }
  });

  textarea.addEventListener('input', function () {
    updateCount();
    autoGrow();
  });

  textarea.addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && e.keyCode !== 229) {
      e.preventDefault();
      if (typeof form.requestSubmit === 'function') {
        form.requestSubmit();
      } else {
        form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      }
    }
  });

  function resetComposer() {
    textarea.value = '';
    updateCount();
    autoGrow();
    clearPreview();
    hideError();
    textarea.focus();
  }

  function setSending(on) {
    sending = on;
    sendBtn.disabled = on;
    sendBtn.classList.toggle('is-sending', on);
    sendBtn.setAttribute('aria-busy', on ? 'true' : 'false');
  }

  /* ---------------- 发送 ---------------- */

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    if (sending) return;

    var content = textarea.value.trim();
    if (!content && !pendingFile) {
      showError('请输入内容或选择图片');
      textarea.focus();
      return;
    }
    if (textarea.value.length > 1000) {
      showError('消息不能超过 1000 字');
      return;
    }

    var fd = new FormData();
    fd.append('_csrf', CSRF);
    fd.append('content', content);
    if (pendingFile) fd.append('image', pendingFile, pendingFile.name || 'image');

    setSending(true);
    hideError();

    fetch('/chat/send', {
      method: 'POST',
      body: fd,
      credentials: 'same-origin',
      headers: {
        'X-CSRF-Token': CSRF,
        'X-Requested-With': 'XMLHttpRequest',
        'Accept': 'application/json'
      }
    }).then(function (res) {
      if (res.status === 401) {
        var err = new Error('登录状态已失效，请刷新页面后重新登录');
        err.auth = true;
        throw err;
      }
      return res.json().catch(function () {
        throw new Error('发送失败（HTTP ' + res.status + '）');
      });
    }).then(function (data) {
      if (!data || !data.ok) throw new Error((data && data.msg) || '发送失败，请稍后重试');
      if (data.message) {
        var id = parseInt(data.message.id, 10);
        if (!(id && seen[id])) {
          stick = true;
          appendMessages([data.message], false);
        }
      }
      resetComposer();
      scrollToBottom(true);
    }).catch(function (err) {
      if (err && err.auth) polling = false;
      showError((err && err.message) || '发送失败，请稍后重试');
    }).then(function () {
      setSending(false);
    }, function () {
      setSending(false);
    });
  });

  updateCount();
  autoGrow();
})();
