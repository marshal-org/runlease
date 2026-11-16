// The Runlease home page. First-party code only: nothing here is fetched from
// another site, and nothing the visitor types leaves the page.
//
// The page is a leased machine. It has a deadline, a running cost and an off
// switch, and when the deadline passes it deletes itself. Around that sit a
// terminal that behaves like the CLI, a diagram that draws what each command
// builds, and a panel that encrypts what you type to show what a relay sees.
(function () {
  'use strict';

  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var params = new URLSearchParams(location.search);
  var now = function () { return Date.now(); };
  var sleep = function (ms) { return new Promise(function (r) { setTimeout(r, ms); }); };
  var rnd = function (a, b) { return a + Math.random() * (b - a); };
  var track = function (event, verb) { try { window.dispatchEvent(new CustomEvent('runlease:track', { detail: { event: event, verb: verb } })); } catch (e) { /* never break the page */ } };
  var hex = function (n) { var s = ''; while (s.length < n) s += Math.floor(Math.random() * 16).toString(16); return s; };

  // "2h", "90m", "45s", "1h30m" -> milliseconds. 0 when it is not a duration.
  function parseDur(s) {
    if (!s) return 0;
    var m, total = 0, re = /(\d+(?:\.\d+)?)(h|m|s)/g, seen = '';
    while ((m = re.exec(s))) { total += parseFloat(m[1]) * { h: 3600e3, m: 60e3, s: 1e3 }[m[2]]; seen += m[0]; }
    return seen === s ? total : 0;
  }
  // The way the CLI prints a duration: 1h0m0s, 29m30s.
  function goDur(ms) {
    var s = Math.max(0, Math.round(ms / 1000)), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
    return (h ? h + 'h' : '') + (h || m ? m + 'm' : '') + r + 's';
  }
  function lease(ms) {
    var s = Math.max(0, Math.round(ms / 1000)), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
    return (h ? h + 'h' : '') + (m ? m + 'm' : '') + (!h && !m ? r + 's' : '');
  }
  function clock(ms) {
    var s = Math.max(0, Math.ceil(ms / 1000)), m = Math.floor(s / 60), r = s % 60;
    return m + ':' + (r < 10 ? '0' : '') + r;
  }
  function human(ms) {
    var s = Math.round(ms / 1000), m = Math.floor(s / 60), r = s % 60;
    return (m ? m + 'm ' : '') + (r < 10 && m ? '0' : '') + r + 's';
  }

  // ------------------------------------------------------------------ ash
  // One fixed canvas over the page. When a part of the page is deleted, it
  // leaves as ash: small squares that rise, drift and fade.
  var ash = (function () {
    var cv = $('#ash'), ctx = cv.getContext('2d'), parts = [], running = false, dpr = 1;
    var palette = ['#E9F0F9', '#B4C3D6', '#7F93AD', '#FFB547', '#7BA2FF'];
    function size() {
      dpr = Math.min(window.devicePixelRatio || 1, 2);
      cv.width = Math.round(window.innerWidth * dpr); cv.height = Math.round(window.innerHeight * dpr);
    }
    function frame() {
      ctx.clearRect(0, 0, cv.width, cv.height);
      var alive = [];
      for (var i = 0; i < parts.length; i++) {
        var p = parts[i];
        p.life += 1;
        if (p.life > p.max) continue;
        p.vx += rnd(-.06, .06); p.vy -= .012;
        p.x += p.vx; p.y += p.vy;
        var k = 1 - p.life / p.max;
        ctx.globalAlpha = Math.max(0, k) * p.a;
        ctx.fillStyle = p.c;
        ctx.fillRect(p.x * dpr, p.y * dpr, p.s * dpr * k + .5, p.s * dpr * k + .5);
        alive.push(p);
      }
      parts = alive;
      ctx.globalAlpha = 1;
      if (parts.length) requestAnimationFrame(frame); else { running = false; cv.classList.remove('on'); }
    }
    // inward = true gathers particles into the rectangle instead of scattering.
    function emit(rect, count, inward, ember) {
      if (reduce) return;
      var top = Math.max(0, rect.top), bottom = Math.min(window.innerHeight, rect.bottom);
      if (bottom <= top) return;
      var n = Math.min(count || Math.round(rect.width * (bottom - top) / 900), 520);
      for (var i = 0; i < n && parts.length < 3200; i++) {
        var x = rect.left + Math.random() * rect.width, y = top + Math.random() * (bottom - top);
        var p = { x: x, y: y, vx: rnd(-.7, .7), vy: rnd(-1.6, -.2), s: rnd(1.2, 3.4), a: rnd(.5, 1), life: 0, max: rnd(50, 130), c: palette[Math.floor(Math.random() * palette.length)] };
        if (inward) { p.x += p.vx * -40; p.y += 60 + p.vy * -30; p.max = rnd(30, 60); }
        if (ember) { p.c = Math.random() < .7 ? '#FFB547' : '#FF8A3D'; p.max = rnd(70, 170); p.vy = rnd(-1.1, -.3); p.s = rnd(1, 2.6); }
        parts.push(p);
      }
      if (!running && parts.length) { running = true; cv.classList.add('on'); requestAnimationFrame(frame); }
    }
    size(); window.addEventListener('resize', size);
    return { emit: emit };
  })();

  // ------------------------------------------------------------- the lease
  var RATE = 0.026; // dollars an hour: a small spot machine, as the CLI quoted it
  var SNOOZE = 180e3;
  var page = { alive: true, startedAt: now(), total: parseDur(params.get('ttl')) || 180e3, lives: 1 };
  page.expiresAt = page.startedAt + page.total;

  var el = {
    barTime: $('#bar-time'), heroTime: $('#hero-time'), cost: $('#bar-cost'), meter: $('#bar-meter'),
    warn: $('#warn'), warnTime: $('#warn-time'), after: $('#after'), afterLog: $('#after-log'),
    afterBig: $('#after-big'), afterSay: $('#after-say'), leaseNo: $('#lease-no'), main: $('#main'), bar: $('#bar')
  };

  // The tab counts down too: its title, and an icon whose ring empties.
  var baseTitle = document.title, icon = document.querySelector('link[rel="icon"]'), iconWas = icon ? icon.href : '', iconAt = 0;
  function tabState(left) {
    document.title = page.alive ? clock(left) + ' · this tab deletes itself · ' + baseTitle : 'deleted itself · ' + baseTitle;
    if (!icon || now() - iconAt < 2000) return; iconAt = now();
    try {
      var c = document.createElement('canvas'); c.width = c.height = 64; var g = c.getContext('2d'), k = Math.max(0, Math.min(1, left / page.total));
      g.fillStyle = '#050B14'; g.beginPath(); g.arc(32, 32, 32, 0, 7); g.fill();
      g.lineWidth = 9; g.strokeStyle = '#1D3049'; g.beginPath(); g.arc(32, 32, 22, 0, 7); g.stroke();
      if (page.alive) { g.strokeStyle = left < 30e3 ? '#FF7A6B' : '#FFB547'; g.lineCap = 'round'; g.beginPath(); g.arc(32, 32, 22, -Math.PI / 2, -Math.PI / 2 + Math.PI * 2 * k); g.stroke(); }
      icon.type = 'image/png'; icon.href = c.toDataURL('image/png');
    } catch (e) { icon.href = iconWas; }
  }

  function pageCost() { return (now() - page.startedAt) / 3600e3 * RATE; }

  function tick() {
    if (!page.alive) return;
    var left = page.expiresAt - now();
    var t = clock(left);
    if (el.barTime.textContent !== t) {
      el.barTime.textContent = t; el.heroTime.textContent = t; el.warnTime.textContent = t;
      el.heroTime.setAttribute('datetime', 'PT' + Math.max(0, Math.ceil(left / 1000)) + 'S');
    }
    el.cost.textContent = '$' + pageCost().toFixed(5);
    el.meter.style.transform = 'scaleX(' + Math.max(0, Math.min(1, left / page.total)) + ')';
    var soon = left <= 30e3 && page.total > 40e3;
    document.body.classList.toggle('expiring', soon);
    el.warn.classList.toggle('on', soon);
    el.warn.setAttribute('aria-hidden', soon ? 'false' : 'true');
    embers(left); tabState(left);
    // A lease that ends while the tab is in the background waits to be seen.
    if (left <= 0 && !document.hidden) terminate('lease');
  }

  // The countdown sheds embers, a few at first and more as the end comes.
  function embers(left) {
    if (reduce || document.hidden) return;
    var r = el.heroTime.getBoundingClientRect();
    if (r.bottom < 0 || r.top > window.innerHeight) return;
    var k = 1 - Math.max(0, Math.min(1, left / page.total)), n = left < 30e3 ? 7 : Math.random() < .25 + k * .6 ? 1 + Math.round(k * 2) : 0;
    if (n) ash.emit({ left: r.left, top: r.top + r.height * .15, width: r.width, bottom: r.top + r.height * .8, height: r.height * .65 }, n, false, true);
  }

  function snoozePage(ms) {
    if (!page.alive) return;
    ms = ms || SNOOZE;
    page.expiresAt = now() + ms; page.total = ms;
    el.meter.style.transition = 'none'; tick();
    requestAnimationFrame(function () { el.meter.style.transition = ''; });
    flash(el.heroTime);
    return ms;
  }

  function flash(node) {
    if (reduce || !node.animate) return;
    node.animate([{ transform: 'scale(1)', filter: 'brightness(1)' }, { transform: 'scale(1.12)', filter: 'brightness(1.6)' }, { transform: 'scale(1)', filter: 'brightness(1)' }], { duration: 520, easing: 'cubic-bezier(.2,.8,.2,1)' });
  }

  // The headline is split into letters so that it can leave one letter at a time.
  var letters = [];
  (function splitHeadline() {
    if (reduce) return;
    var h1 = $('#h1');
    Array.prototype.slice.call(h1.childNodes).forEach(function (node) {
      if (node.nodeType !== 3) { letters.push(node); return; }
      var frag = document.createDocumentFragment();
      node.textContent.split(/(\s+)/).forEach(function (word) {
        if (!word) return;
        if (/^\s+$/.test(word)) { frag.appendChild(document.createTextNode(' ')); return; }
        var w = document.createElement('span'); w.style.whiteSpace = 'nowrap'; w.style.display = 'inline-block';
        word.split('').forEach(function (c) { var s = document.createElement('span'); s.className = 'ch'; s.textContent = c; w.appendChild(s); letters.push(s); });
        frag.appendChild(w);
      });
      h1.replaceChild(frag, node);
    });
  })();
  var letterAnims = [];
  function scatterLetters() {
    letters.forEach(function (s, i) {
      if (!s.animate) return;
      letterAnims.push(s.animate([
        { transform: 'none', opacity: 1, filter: 'blur(0)' },
        { transform: 'translate(' + rnd(-90, 90) + 'px,' + rnd(-160, -30) + 'px) rotate(' + rnd(-70, 70) + 'deg) scale(.4)', opacity: 0, filter: 'blur(5px)' }
      ], { duration: rnd(700, 1300), delay: i * 14, easing: 'cubic-bezier(.4,0,.9,.5)', fill: 'forwards' }));
    });
  }

  var dying = false;
  function terminate(reason) {
    if (!page.alive || dying) return;
    dying = true; page.alive = false; iconAt = 0; tabState(0);
    track(reason === 'down' ? 'site_deleted_by_hand' : 'site_lease_ended');
    var lived = now() - page.startedAt, cost = pageCost();
    el.warn.classList.remove('on');
    document.body.classList.remove('expiring');
    try { window.dispatchEvent(new CustomEvent('runlease:page', { detail: { state: 'terminating', reason: reason } })); } catch (e) { /* old browser */ }

    var parts = $$('[data-part], .perf', el.main);
    var vh = window.innerHeight;
    // The page does not blink out. It is taken apart piece by piece, from the
    // bottom of the screen upward, each piece leaving as ash, the headline
    // letter by letter, and the status bar last.
    var GRAINS = '.eyebrow, .lede, .acts > *, .ticks li, h2, .stage, .fact, .wire > div, .cc > div, .use, .none > div, .get pre, .links li, .fine, .switch, .verdict, .by .wrap > *, footer .wrap > *, .perf';
    var grains = $$(GRAINS, el.main).filter(function (g) { var r = g.getBoundingClientRect(); return r.bottom > 0 && r.top < vh && r.width > 0; })
      .sort(function (a, b) { var ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect(); return (rb.top - ra.top) || (rb.left - ra.left); });
    var h1 = $('#h1'), h1r = h1.getBoundingClientRect(), h1Visible = h1r.bottom > 0 && h1r.top < vh;
    var step = reduce ? 0 : Math.max(45, Math.min(130, 2400 / Math.max(1, grains.length))), t = reduce ? 0 : 500;
    document.body.classList.add('ending');
    grains.forEach(function (g) {
      setTimeout(function () { ash.emit(g.getBoundingClientRect()); g.classList.add('going'); }, t);
      t += step;
    });
    if (h1Visible) { setTimeout(function () { ash.emit(h1r, 420); scatterLetters(); if (reduce) h1.classList.add('going'); }, t); t += reduce ? 0 : 1250; }
    setTimeout(function () {
      ash.emit(el.bar.getBoundingClientRect(), 200);
      el.bar.classList.add('going');
    }, t);
    setTimeout(function () {
      $$('.going', el.main).forEach(function (g) { g.classList.remove('going'); });
      parts.forEach(function (p) { p.classList.add('gone'); });
      el.bar.classList.remove('going'); el.bar.classList.add('gone');
      document.body.classList.remove('ending');
      window.scrollTo(0, 0);
      showAfter(reason, lived, cost);
      dying = false;
    }, t + (reduce ? 50 : 1000));
  }

  function line(html) { var d = document.createElement('div'); d.innerHTML = html; el.afterLog.appendChild(d); return d; }
  async function showAfter(reason, lived, cost) {
    document.body.classList.add('dead');
    el.afterLog.innerHTML = '';
    el.afterBig.style.opacity = 0; el.afterSay.style.opacity = 0;
    var acts = $('.after-acts'); acts.style.opacity = 0;
    el.afterBig.innerHTML = reason === 'down'
      ? 'You deleted it. <em>It would have gone anyway.</em>'
      : 'It deleted itself. <em>That is the product.</em>';
    el.afterSay.textContent = 'this-website lived ' + human(lived) + ', cost $' + cost.toFixed(5) +
      ' at the spot price of a small machine, and left nothing behind. Nobody had to remember to turn it off. ' +
      'Every Runlease machine ends the same way, in your own cloud account.';
    var gap = reduce ? 0 : 380;
    await sleep(gap);
    line(reason === 'down'
      ? '<span class="p">$</span> runlease down this-website --yes'
      : '<span class="d">⏰ lease over: this-website deleted itself</span>');
    await sleep(gap);
    if (reason === 'down') { line('🗑️  Terminating this-website...'); await sleep(gap); line('<span class="ok">✅ Terminated.</span>'); await sleep(gap); }
    line('<span class="p">$</span> runlease list');
    await sleep(gap);
    line('No active instances tracked on this computer.');
    await sleep(gap);
    line('<span class="p">$</span> <span class="cur"></span>');
    [el.afterBig, el.afterSay, acts].forEach(function (n, i) {
      n.style.transition = 'opacity .7s ease ' + (i * .18) + 's'; n.style.opacity = 1;
    });
    var first = $('[data-act="up"]', el.after); if (first) first.focus({ preventScroll: true });
  }

  function revive() {
    if (page.alive || dying) return;
    document.body.classList.remove('dead');
    letterAnims.forEach(function (a) { a.cancel(); }); letterAnims = [];
    var parts = $$('[data-part], .perf', el.main);
    el.bar.classList.remove('gone');
    parts.forEach(function (p, i) {
      p.classList.remove('gone');
      if (!reduce && i < 4) { p.classList.add('coming'); p.style.animationDelay = (i * 120) + 'ms'; setTimeout(function () { p.classList.remove('coming'); p.style.animationDelay = ''; }, 1400); }
    });
    ash.emit($('.hero').getBoundingClientRect(), 320, true);
    track('site_revived');
    page.lives += 1; page.alive = true; iconAt = 0; page.startedAt = now(); page.total = 300e3; page.expiresAt = page.startedAt + page.total;
    el.leaseNo.textContent = ('000' + page.lives).slice(-4);
    tick();
    try { window.dispatchEvent(new CustomEvent('runlease:page', { detail: { state: 'running' } })); } catch (e) { /* old browser */ }
  }

  try {
    console.log('%crunlease%c  this page is on a lease. So is every machine it launches.\n' +
      'Try the terminal further down, or run this yourself:\n  curl -fsSL https://runlease.online/install.sh | sh\n' +
      'Source: https://github.com/marshal-org/runlease',
      'font:800 22px system-ui;color:#FFB547', 'font:13px ui-monospace,monospace;color:#7F93AD');
  } catch (e) { /* no console */ }
  setInterval(tick, 250); tick();
  document.addEventListener('visibilitychange', tick);

  document.addEventListener('click', function (e) {
    var b = e.target.closest ? e.target.closest('[data-act]') : null;
    if (!b) return;
    var act = b.getAttribute('data-act');
    if (act === 'snooze') { track('site_snoozed'); var ms = snoozePage(); if (ms) term.note('😴 this-website now auto-terminates in ' + goDur(ms)); }
    if (act === 'down') terminate('down');
    if (act === 'up') revive();
  });

  // Copy buttons. "Copied" is only ever shown after the write resolved.
  document.addEventListener('click', function (e) {
    var b = e.target.closest ? e.target.closest('[data-copy]') : null;
    if (!b) return;
    var src = document.getElementById(b.getAttribute('data-copy')); if (!src) return;
    var text = src.textContent, was = b.getAttribute('data-label') || b.textContent; b.setAttribute('data-label', was);
    var done = function (msg) { b.textContent = msg; setTimeout(function () { b.textContent = was; }, 1600); };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text).then(function () { done('Copied'); }, function () { select(src); done('Select + copy'); });
    else { select(src); done('Select + copy'); }
  });
  function select(node) { var r = document.createRange(); r.selectNodeContents(node); var s = window.getSelection(); s.removeAllRanges(); s.addRange(r); }

  $('#share').addEventListener('click', function () {
    track('site_shared');
    var b = this, was = b.getAttribute('data-label') || b.textContent, url = 'https://runlease.online/'; b.setAttribute('data-label', was);
    var done = function (msg) { b.textContent = msg; setTimeout(function () { b.textContent = was; }, 2200); };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(url).then(function () { done('Link copied: ' + url); }, function () { done(url); });
    else done(url);
  });

  // Sections rise into place the first time they are seen.
  if ('IntersectionObserver' in window && !reduce) {
    var io = new IntersectionObserver(function (es) { es.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add('in'); io.unobserve(e.target); } }); }, { rootMargin: '0px 0px -8% 0px' });
    $$('.rise').forEach(function (n) { io.observe(n); });
  } else $$('.rise').forEach(function (n) { n.classList.add('in'); });

  // -------------------------------------------------------- the simulation
  // Machines the visitor launches. Time runs sixty times faster than real
  // time, so that a lease can be watched to its end.
  var SCALE = 60, LIMIT = 8;
  var RATES = { 'compute.small': 0.026, 'compute.large': 0.080, 'compute.gpu': 0.102 };
  var TYPES = { gcp: { 'compute.small': 'e2-small', 'compute.large': 'e2-standard-4' }, aws: {} };
  var SIZES = ['compute.micro', 'compute.small', 'compute.medium', 'compute.large', 'compute.xlarge', 'compute.2xlarge', 'compute.gpu'];
  var sim = {
    machines: [], listeners: [], lid: true, breached: false, missed: [],
    on: function (fn) { this.listeners.push(fn); },
    emit: function (type, m, extra) { this.listeners.forEach(function (fn) { fn(type, m, extra); }); },
    get: function (name) { for (var i = 0; i < this.machines.length; i++) if (this.machines[i].name === name) return this.machines[i]; return null; },
    left: function (m) { return (m.expiresAt - now()) * SCALE; }, // in simulated milliseconds
    launch: function (o) {
      var m = { name: o.name, id: 'rl-' + o.name + '-' + hex(6), provider: o.provider, size: o.size, region: o.provider === 'aws' ? 'us-east-1' : 'us-central1-a', state: 'pending', bornAt: now(), total: o.ttl / SCALE, expiresAt: now() + o.ttl / SCALE };
      this.machines.push(m); this.emit('launch', m); return m;
    },
    ready: function (m) { if (m.state === 'pending') { m.state = 'running'; this.emit('ready', m); } },
    snooze: function (m, ttl) { m.total = ttl / SCALE; m.expiresAt = now() + m.total; this.emit('snooze', m); },
    remove: function (m, why) {
      if (m.state === 'gone') return;
      m.state = 'gone'; this.machines = this.machines.filter(function (x) { return x !== m; });
      this.emit(why === 'down' ? 'down' : 'expire', m);
    },
    tick: function () {
      var t = now(), self = this;
      this.machines.slice().forEach(function (m) {
        if (m.state !== 'gone' && m.expiresAt <= t) {
          self.remove(m, 'lease');
          if (self.lid) term.note('⏰ ' + m.name + ': lease over, deleted itself', 'am'); else self.missed.push(m.name);
        }
      });
    }
  };
  setInterval(function () { sim.tick(); }, 200);

  // ------------------------------------------------------------ the terminal
  var term = (function () {
    var out = $('#term-out'), input = $('#term-input'), history = [], hpos = 0, queue = Promise.resolve(), busy = false;
    function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }
    function add(text, cls) {
      var d = document.createElement('div'); d.className = 'l' + (cls ? ' ' + cls : ''); d.innerHTML = text;
      out.appendChild(d);
      while (out.childNodes.length > 400) out.removeChild(out.firstChild);
      out.scrollTop = out.scrollHeight; return d;
    }
    function print(text, cls) { return add(esc(text), cls); }
    function note(text, cls) { return add(esc(text), cls || 'd'); }
    var pause = function (ms) { return sleep(reduce ? 0 : ms); };

    async function typeCmd(cmd) {
      var d = add('<span class="p">$</span> ');
      var span = document.createElement('span'); d.appendChild(span);
      if (reduce) { span.textContent = cmd; return; }
      for (var i = 0; i < cmd.length; i++) { span.textContent += cmd[i]; out.scrollTop = out.scrollHeight; await sleep(i % 3 === 0 ? 22 : 9); }
      await sleep(160);
    }

    function tokens(s) {
      var t = [], m, re = /"([^"]*)"|'([^']*)'|(\S+)/g;
      while ((m = re.exec(s))) t.push(m[1] !== undefined ? m[1] : m[2] !== undefined ? m[2] : m[3]);
      return t;
    }
    function flags(args, spec) {
      // spec: { '--ttl': 'ttl', '-n': 'name', '-d': true } -> options and the rest
      var o = { _: [] };
      for (var i = 0; i < args.length; i++) {
        var a = args[i], eq = a.indexOf('='), key = eq > 0 && a[0] === '-' ? a.slice(0, eq) : a;
        if (key === '--') { o.rest = args.slice(i + 1); break; }
        if (!(key in spec)) { if (a[0] === '-') return { error: 'unknown flag: ' + a }; o._.push(a); continue; }
        if (spec[key] === true) { o[key.replace(/^-+/, '')] = true; continue; }
        var v = eq > 0 ? a.slice(eq + 1) : args[++i];
        if (v === undefined) return { error: 'flag needs an argument: ' + key };
        o[spec[key]] = v;
      }
      return o;
    }
    function err(msg) { print('Error: ' + msg, 'er'); }

    var HELP = [
      'A simulation of the runlease CLI. Nothing here is real or billed.',
      '',
      '  runlease up [-p gcp|aws] [-n NAME] [--ttl 2h] [-s SIZE] -d   launch a spot machine',
      '  runlease exec NAME -- COMMAND                              run something on it',
      '  runlease push NAME LOCAL REMOTE   ·   pull NAME REMOTE LOCAL',
      '  runlease snooze NAME 1h                                    move the deadline',
      '  runlease list                                              what is running',
      '  runlease down NAME --yes                                   delete it now',
      '  runlease doctor   ·   whoami   ·   version',
      '',
      'Try:  runlease down this-website --yes',
    ];

    function table() {
      var rows = [['NAME', 'PROV', 'STATE', 'REGION', 'EXPIRES', '$/HR']];
      sim.machines.forEach(function (m) { rows.push([m.name, m.provider, m.state, m.region, goDur(sim.left(m)), RATES[m.size] ? '~' + RATES[m.size].toFixed(3) : '-']); });
      if (page.alive) rows.push(['this-website', 'web', 'running', 'your tab', goDur(page.expiresAt - now()), '~' + RATE.toFixed(3)]);
      if (rows.length === 1) { print('No active instances tracked on this computer.'); return; }
      var w = rows[0].map(function (_, c) { return Math.max.apply(null, rows.map(function (r) { return r[c].length; })); });
      rows.forEach(function (r, i) { print(r.map(function (c, j) { return c + new Array(w[j] - c.length + 1).join(' '); }).join('  ').replace(/\s+$/, ''), i === 0 ? 'd' : ''); });
    }

    async function up(args) {
      var o = flags(args, { '-p': 'provider', '--provider': 'provider', '-n': 'name', '--name': 'name', '--ttl': 'ttl', '-s': 'size', '--size': 'size', '-r': 'region', '--region': 'region', '-d': true, '--detach': true, '--public-ip': true });
      if (o.error) return err(o.error);
      var provider = o.provider || 'gcp', size = o.size || 'compute.small', ttl = parseDur(o.ttl || '2h');
      if (provider !== 'gcp' && provider !== 'aws') return err('unknown provider "' + provider + '" — supported: aws, gcp');
      if (SIZES.indexOf(size) < 0) return err('unknown instance class "' + size + '". Classes: ' + SIZES.join(', '));
      if (!ttl) return err('invalid --ttl "' + o.ttl + '": use a duration such as 30m or 2h');
      var name = o.name || 'rl-spot-' + hex(6);
      if (!/^[a-z0-9][a-z0-9-]{0,40}$/.test(name)) return err('invalid name "' + name + '": lowercase letters, digits and dashes');
      if (name === 'this-website') return err('a machine named "this-website" is already tracked: you are looking at it');
      if (sim.get(name)) return err('a machine named "' + name + '" is already tracked (' + sim.get(name).id + ', running).\nRun \'runlease down ' + name + '\' first, or launch under another name (--name)');
      if (sim.machines.length >= LIMIT) return err('relay: tier limit reached (' + LIMIT + ' of ' + LIMIT + ' machines in use in this demo).\nRun \'runlease down NAME\' to free a place');
      var type = (TYPES[provider] || {})[size] || size, region = provider === 'aws' ? 'us-east-1' : 'us-central1-a';
      print(provider === 'aws' ? '☁  AWS account 123456789012, region us-east-1, as you' : '☁  Google Cloud project your-project, zone us-central1-a, as you');
      await pause(260);
      print('📡 Pre-registering relay slot...');
      await pause(380);
      print('⚡ Requesting spot instance (' + type + ' in ' + region + ')...');
      var m = sim.launch({ name: name, provider: provider, size: size, ttl: ttl });
      await pause(420);
      print('   └─ ' + m.id + ' — waiting for running state...', 'd');
      await pause(900);
      if (m.state === 'gone') return;
      sim.ready(m);
      print('');
      print('✅ Instance ready!', 'ok');
      print('   Name:      ' + name);
      print('   Expires:   in ' + goDur(ttl));
      if (RATES[size]) print('   Rate:      ~$' + RATES[size].toFixed(3) + '/hr · ~$' + (RATES[size] * ttl / 3600e3).toFixed(3) + ' for the ' + lease(ttl) + ' lease');
      print('   It deletes itself when the lease ends.', 'd');
    }

    function target(name) {
      var m = sim.get(name);
      if (!m) { err('instance "' + name + '" not found — run \'runlease list\''); return null; }
      if (m.state !== 'running') { err(name + ' is still starting: try again in a moment'); return null; }
      return m;
    }

    async function exec(args) {
      var name = args[0], sep = args.indexOf('--');
      if (!name || sep < 0 || sep === args.length - 1) return err('usage: runlease exec NAME -- COMMAND');
      var m = target(name); if (!m) return;
      var cmd = args.slice(sep + 1), line = cmd.join(' ');
      sim.emit('exec', m);
      await pause(520);
      if (/id_(rsa|ed25519)|\.ssh\/|\.aws|credentials/.test(line)) { print('cat: no such file or directory', 'er'); print('  (your private key was never on this machine)', 'd'); return; }
      if (cmd[0] === 'sudo') { print('sudo: a password is required', 'er'); print('  (sudo is locked unless the manifest opts in)', 'd'); return; }
      if (/^rm\s+-rf\s+\/(\s|$)/.test(line)) { print('Go ahead. It is gone in ' + goDur(sim.left(m)) + ' either way.', 'd'); return; }
      if (line === 'uname -a') return print('Linux ' + m.id + ' 6.1.0-54-cloud-amd64 #1 SMP PREEMPT_DYNAMIC Debian x86_64 GNU/Linux');
      if (line === 'whoami') return print('runlease-agent');
      if (line === 'hostname') return print(m.id);
      if (line === 'nproc') return print({ 'compute.micro': '2', 'compute.small': '2', 'compute.medium': '2', 'compute.large': '4', 'compute.xlarge': '8', 'compute.2xlarge': '16', 'compute.gpu': '8' }[m.size]);
      if (cmd[0] === 'echo') return print(cmd.slice(1).join(' '));
      if (cmd[0] === 'nvidia-smi') { if (m.size !== 'compute.gpu') return print('bash: nvidia-smi: command not found  (launch with -s compute.gpu)', 'er'); print('| NVIDIA-SMI    GPU 0: on   |   0MiB / 15360MiB |   0% |'); return; }
      if (/^make\b/.test(line) || /test/.test(line)) {
        var pk = ['core', 'api', 'relay', 'lease'];
        for (var i = 0; i < pk.length; i++) { await pause(260); sim.emit('exec', m); print('ok  \tapp/' + pk[i] + '\t' + rnd(.1, 1.9).toFixed(3) + 's'); }
        await pause(200); return print('PASS', 'ok');
      }
      if (/^bash\b|\.sh$/.test(line)) { await pause(300); print('+ ./install-something-from-the-internet'); await pause(400); return print('done. It ran there, not on your laptop.', 'd'); }
      print('(ran on ' + m.name + ', exit 0)', 'd');
    }

    async function transfer(verb, args) {
      if (args.length < 3) return err('usage: runlease ' + verb + ' NAME ' + (verb === 'push' ? 'LOCAL REMOTE' : 'REMOTE LOCAL'));
      var m = target(args[0]); if (!m) return;
      sim.emit('exec', m); await pause(500);
      print((verb === 'push' ? '📤 ' : '📥 ') + args[1] + ' → ' + args[2] + ' (' + Math.round(rnd(200, 90000)) + ' bytes)');
    }

    function snooze(args) {
      var name = args[0], d = args[1];
      if (args.length === 1) { d = name; name = sim.machines.length === 1 ? sim.machines[0].name : null; if (!name) return err('more than one machine is running: name the one to snooze'); }
      var ttl = parseDur(d);
      if (!name || !ttl) return err('usage: runlease snooze NAME DURATION   (for example: 1h)');
      if (name === 'this-website') { snoozePage(Math.min(ttl, 600e3)); print('😴 this-website now auto-terminates in ' + goDur(Math.min(ttl, 600e3)) + (ttl > 600e3 ? '  (ten minutes is the most a web page gets)' : '')); return; }
      var m = sim.get(name); if (!m) return err('instance "' + name + '" not found — run \'runlease list\'');
      sim.snooze(m, ttl);
      print('😴 ' + name + ' now auto-terminates in ' + goDur(ttl));
    }

    async function down(args) {
      var o = flags(args, { '--yes': true, '-y': true, '--leaked': true });
      if (o.error) return err(o.error);
      if (o.leaked) return print('Nothing leaked: no stopped or expired runlease machines found outside the registry.');
      var name = o._[0];
      if (!name) { if (sim.machines.length === 1) name = sim.machines[0].name; else return err('name the machine to delete: runlease down NAME --yes'); }
      if (name === 'this-website') { print('🗑️  Terminating this-website...'); await pause(500); terminate('down'); return; }
      var m = sim.get(name); if (!m) return err('instance "' + name + '" not found — run \'runlease list\'');
      print('🗑️  Terminating ' + m.name + ' (' + m.id + ')...');
      await pause(520);
      sim.remove(m, 'down');
      print('✅ Terminated.', 'ok');
    }

    async function doctor() {
      var rows = [
        ['ok', 'CLI version: v0.6.0 (your browser)'],
        ['ok', 'Relay sign-in: signed in as you, tier demo, ' + sim.machines.length + ' of ' + LIMIT + ' machines in use'],
        ['ok', 'Relay agent: the relay serves the agent this CLI expects'],
        ['ok', 'Cloud credentials: project your-project, as you'],
        ['warn', 'Reality: this terminal is a simulation, so nothing above was checked'],
      ];
      for (var i = 0; i < rows.length; i++) { await pause(170); print((rows[i][0] === 'ok' ? 'ok    ' : 'warn  ') + rows[i][1], rows[i][0] === 'ok' ? '' : 'am'); }
      print('      fix: curl -fsSL https://runlease.online/install.sh | sh', 'd');
    }

    async function runlease(args) {
      var sub = args[0], rest = args.slice(1);
      switch (sub) {
        case undefined: case 'help': case '--help': case '-h': HELP.forEach(function (l) { print(l, l[0] === ' ' ? '' : 'd'); }); return;
        case 'up': return up(rest);
        case 'exec': return exec(rest);
        case 'push': case 'pull': return transfer(sub, rest);
        case 'snooze': return snooze(rest);
        case 'down': return down(rest);
        case 'list': case 'ls': return table();
        case 'sync': return print('Synced ' + sim.machines.length + ' instance(s).');
        case 'doctor': return doctor();
        case 'whoami': print('you on wss://connect.runlease.online (simulated)'); return print('tier: demo (' + sim.machines.length + ' of ' + LIMIT + ' machines in use, time ×' + SCALE + ')');
        case 'version': return print('v0.6.0 (the one in this page is a simulation)');
        case 'login': return print('Already signed in, in the sense that none of this is real.', 'd');
        case 'connect': case 'ssh': return print('An interactive shell is the one thing a web page cannot fake well. Install it: it takes ten seconds.', 'd');
        case 'forward': if (rest[0] && !target(rest[0])) return; if (rest[0]) sim.emit('exec', sim.get(rest[0])); print('🔀 -L 127.0.0.1:3000 → localhost:3000 (via SSH)'); return print('Reached through the tunnel. No port is opened on the machine.', 'd');
        case 'mcp': return print('Typed tools for agents: runlease_up, runlease_exec, runlease_down and six more.', 'd');
        default: return err('unknown command "' + sub + '" for "runlease". Try: runlease help');
      }
    }

    async function run(cmd) {
      cmd = cmd.trim(); if (!cmd) return;
      var t = tokens(cmd), c = t[0];
      if (c === 'runlease') { track('site_command', t[1] || 'help'); return runlease(t.slice(1)); }
      if (c === 'help' || c === '?') return runlease(['help']);
      if (c === 'clear') { out.innerHTML = ''; return; }
      if (c === 'ls') return print('README.md  src  .ssh');
      if (c === 'pwd') return print('/home/you');
      if (c === 'whoami') return print('you');
      if (c === 'date') return print(new Date().toString());
      if (c === 'echo') return print(t.slice(1).join(' '));
      if (c === 'exit') { print('logout', 'd'); await pause(300); return terminate('down'); }
      if (c === 'sudo') return print('Nice try. This is your laptop, and you already have root on it.', 'd');
      if (c === 'rm') return print('That is what the lease is for. Wait ' + goDur(page.expiresAt - now()) + ' and it does it for you.', 'd');
      if (c === 'curl' || c === 'wget') return print('That one is real. Paste it in your own terminal.', 'd');
      if (c === 'cat' && /ssh|key/.test(cmd)) return print('-----BEGIN OPENSSH PRIVATE KEY-----  (it stays here. That is the point.)', 'd');
      if (c === 'up' || c === 'down' || c === 'list' || c === 'exec' || c === 'snooze' || c === 'doctor') return runlease(t);
      err('command not found: ' + c + '. This terminal only knows runlease. Try: help');
    }

    // Everything runs in order, whether typed, tapped or scripted.
    function submit(cmd, typed, guard) {
      queue = queue.then(async function () {
        if (guard && !guard()) return;
        busy = true;
        if (!sim.lid) { note('(the laptop is closed: nothing can be typed. The machines do not care.)'); busy = false; return; }
        if (typed) add('<span class="p">$</span> ' + esc(cmd)); else await typeCmd(cmd);
        try { await run(cmd); } catch (e) { err('the simulation tripped: ' + e.message); }
        busy = false;
      });
      return queue;
    }

    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') {
        var v = input.value; input.value = '';
        if (v.trim()) { history.push(v); hpos = history.length; submit(v, true); }
      } else if (e.key === 'ArrowUp') { if (hpos > 0) { hpos--; input.value = history[hpos]; e.preventDefault(); }
      } else if (e.key === 'ArrowDown') { if (hpos < history.length) { hpos++; input.value = history[hpos] || ''; e.preventDefault(); }
      } else if (e.key === 'Tab') {
        var parts = input.value.split(' '), last = parts[parts.length - 1];
        var pool = parts.length === 1 ? ['runlease', 'help', 'clear'] : parts.length === 2 ? ['up', 'exec', 'list', 'snooze', 'down', 'doctor', 'whoami', 'push', 'pull', 'help'] : sim.machines.map(function (m) { return m.name; }).concat(['this-website']);
        var hit = pool.filter(function (w) { return w.indexOf(last) === 0; });
        if (!e.shiftKey && last && hit.length === 1) { parts[parts.length - 1] = hit[0]; input.value = parts.join(' ') + ' '; e.preventDefault(); }
      } else if (e.key === 'l' && e.ctrlKey) { out.innerHTML = ''; e.preventDefault(); }
    });
    out.addEventListener('click', function () { if (!String(window.getSelection())) input.focus({ preventScroll: true }); });

    return { submit: submit, note: note, print: print, input: input, isBusy: function () { return busy; } };
  })();

  // Chips and "Run it" buttons.
  var SCRIPTS = {
    agent: ['runlease up -n agent-7 --ttl 2h -d', 'runlease exec agent-7 -- make test'],
    sandbox: ['runlease up -n sandbox --ttl 1h -d', 'runlease push sandbox ./run.sh /tmp/run.sh', 'runlease exec sandbox -- bash /tmp/run.sh'],
    build: ['runlease up -n build -s compute.2xlarge --ttl 30m -d', 'runlease exec build -- make -j16 test'],
    fanout: ['runlease up -n shard-1 --ttl 1h -d', 'runlease up -n shard-2 --ttl 1h -d', 'runlease up -n shard-3 --ttl 1h -d', 'runlease up -n shard-4 --ttl 1h -d', 'runlease list'],
    gpu: ['runlease up -n gpu-job -s compute.gpu --ttl 3h -d', 'runlease exec gpu-job -- nvidia-smi'],
    preview: ['runlease up -n demo --ttl 2h -d', 'runlease forward demo -L 3000:localhost:3000']
  };
  function play(name) {
    var steps = SCRIPTS[name]; if (!steps) return;
    steps.forEach(function (cmd) {
      var m = /^runlease up -n (\S+)/.exec(cmd);
      // A script run twice reuses the machine it already has.
      term.submit(cmd, false, m ? function () { return !sim.get(m[1]); } : null);
    });
  }
  document.addEventListener('click', function (e) {
    var b = e.target.closest ? e.target.closest('[data-run], [data-script]') : null;
    if (!b) return;
    var stage = $('.stage'), r = stage.getBoundingClientRect();
    if (r.top < 60 || r.top > window.innerHeight * .5) window.scrollTo({ top: window.scrollY + r.top - 76, behavior: reduce ? 'auto' : 'smooth' });
    if (b.hasAttribute('data-script')) play(b.getAttribute('data-script')); else term.submit(b.getAttribute('data-run'));
  });

  // The laptop lid. Closing it changes nothing for the machines.
  var lid = $('#lid');
  lid.addEventListener('click', function () {
    sim.lid = !sim.lid; if (!sim.lid) track('site_laptop_closed');
    lid.setAttribute('aria-pressed', sim.lid ? 'false' : 'true');
    lid.textContent = sim.lid ? 'Close the laptop' : 'Open the laptop';
    term.input.disabled = !sim.lid;
    term.input.placeholder = sim.lid ? 'runlease up' : 'laptop closed';
    sim.emit('lid');
    if (sim.lid) {
      if (sim.missed.length) term.note('While the laptop was closed: ' + sim.missed.join(', ') + ' deleted ' + (sim.missed.length > 1 ? 'themselves' : 'itself') + ' on time. Nothing here had to be running.', 'am');
      else term.note('Laptop open again. Every machine kept its own time while you were away.');
      sim.missed = [];
    }
  });

  // Breaking the relay.
  var breach = $('#breach'), verdict = $('#verdict'), verdictWas = verdict.innerHTML;
  function setBreach(on) {
    sim.breached = on; if (on) track('site_relay_broken');
    breach.setAttribute('aria-pressed', on ? 'true' : 'false');
    breach.textContent = on ? 'Relay compromised — undo' : 'Compromise the relay';
    document.body.classList.toggle('breached', on);
    verdict.innerHTML = on ? 'The attacker is in. Their haul: ciphertext, and a list of connection times. Your sessions are still running.' : verdictWas;
    sim.emit('breach');
  }
  breach.addEventListener('click', function () { setBreach(!sim.breached); });

  // ------------------------------------------------------- the blind relay
  (function wire() {
    var input = $('#wire-in'), hexEl = $('#wire-hex'), outEl = $('#wire-out'), meta = $('#wire-meta'), key = null, seq = 0;
    var subtle = window.crypto && window.crypto.subtle;
    function toHex(buf) { var b = new Uint8Array(buf), s = ''; for (var i = 0; i < b.length && i < 220; i++) s += (b[i] < 16 ? '0' : '') + b[i].toString(16) + ((i + 1) % 2 === 0 ? ' ' : ''); return s + (b.length > 220 ? '…' : ''); }
    async function update() {
      var text = input.value, mine = ++seq;
      if (!subtle) { hexEl.textContent = hex(Math.max(32, text.length * 2 + 32)).replace(/(.{4})/g, '$1 '); outEl.textContent = text; return; }
      try {
        if (!key) key = await subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt', 'decrypt']);
        var iv = window.crypto.getRandomValues(new Uint8Array(12));
        var ct = await subtle.encrypt({ name: 'AES-GCM', iv: iv }, key, new TextEncoder().encode(text));
        var pt = await subtle.decrypt({ name: 'AES-GCM', iv: iv }, key, ct);
        if (mine !== seq) return;
        hexEl.textContent = toHex(ct);
        meta.textContent = ct.byteLength + ' bytes of ciphertext · it changes completely with every keystroke';
        outEl.textContent = new TextDecoder().decode(pt);
        sim.emit('wire');
      } catch (e) { outEl.textContent = text; }
    }
    input.addEventListener('input', update); update();
  })();

  // The diagram is drawn by scene.js-in-this-file, below. It only listens.
  window.__runlease = { sim: sim, term: term, page: page, SCALE: SCALE, reduce: reduce, goDur: goDur, snoozePage: snoozePage, setBreach: setBreach, lid: lid, ash: ash };
})();

// ------------------------------------------------------------------ the scene
// A live diagram of what the terminal builds: your machine, the relay, and the
// spot machines in your cloud account. It draws the simulation and nothing
// else, so what it shows is always what the commands did.
(function () {
  'use strict';
  var R = window.__runlease; if (!R) return;
  var sim = R.sim, term = R.term, page = R.page, reduce = R.reduce;
  var cv = document.getElementById('scene-canvas'); if (!cv || !cv.getContext) return;
  var ctx = cv.getContext('2d'); if (!ctx) return;
  var W = 0, H = 0, dpr = 1, L = null, visible = false, mouse = { x: -1, y: -1 }, hot = null;
  var C = { void: '#03070D', deck: '#0A1524', deck2: '#0F1D31', rule: '#1D3049', rule2: '#2B4466', ink: '#E9F0F9', ink2: '#B4C3D6', ink3: '#7F93AD', lease: '#FFB547', signal: '#7BA2FF', ok: '#4CC38A', no: '#FF7A6B' };
  var MONO = '"IBM Plex Mono", ui-monospace, Menlo, monospace';
  var slots = [null, null, null, null, null, null, null, null];
  var vis = {}, ghosts = [], packets = [], floats = [], sparks = [], keys = [], hits = [], relayPulse = 0, sees = '', seesAt = 0;
  var now = function () { return Date.now(); };
  var rnd = function (a, b) { return a + Math.random() * (b - a); };

  function resize() {
    var r = cv.getBoundingClientRect();
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    W = Math.max(200, r.width); H = Math.max(200, r.height);
    cv.width = Math.round(W * dpr); cv.height = Math.round(H * dpr);
    layout();
  }
  function layout() {
    var wide = W >= 520;
    L = { wide: wide };
    if (wide) {
      L.you = { x: W * .14, y: H * .34, w: 118, h: 76 };
      L.web = { x: W * .14, y: H * .72, w: 146, h: 54 };
      L.relay = { x: W * .37, y: H * .34, r: 36 };
      L.cloud = { x: W * .52, y: 18, w: W * .48 - 14, h: H - 62 };
      L.cols = L.cloud.w >= 330 ? 2 : 1;
    } else {
      L.you = { x: W * .23, y: 44, w: 96, h: 56 };
      L.web = { x: W * .23, y: 138, w: 128, h: 44 };
      L.relay = { x: W * .7, y: 60, r: 28 };
      L.cloud = { x: 10, y: 178, w: W - 20, h: H - 178 - 62 };
      L.cols = 2;
    }
    L.rows = 8 / L.cols;
    var padX = 12, padTop = 30, gap = 8;
    L.tw = (L.cloud.w - padX * 2 - gap * (L.cols - 1)) / L.cols;
    L.th = Math.min(56, (L.cloud.h - padTop - 10 - gap * (L.rows - 1)) / L.rows);
    Object.keys(vis).forEach(function (n) { vis[n].relayout = true; });
    L.slot = function (i) {
      var c = i % L.cols, r = Math.floor(i / L.cols);
      return { x: L.cloud.x + padX + c * (L.tw + gap), y: L.cloud.y + padTop + r * (L.th + gap) };
    };
  }

  function rr(x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y); ctx.lineTo(x + w - r, y); ctx.quadraticCurveTo(x + w, y, x + w, y + r);
    ctx.lineTo(x + w, y + h - r); ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
    ctx.lineTo(x + r, y + h); ctx.quadraticCurveTo(x, y + h, x, y + h - r);
    ctx.lineTo(x, y + r); ctx.quadraticCurveTo(x, y, x + r, y); ctx.closePath();
  }
  // The ticket: a rounded card with one corner punched off, as in the logo.
  function ticket(x, y, w, h) {
    var r = 6, cut = Math.min(14, h * .32);
    ctx.beginPath();
    ctx.moveTo(x + r, y); ctx.lineTo(x + w - r, y); ctx.quadraticCurveTo(x + w, y, x + w, y + r);
    ctx.lineTo(x + w, y + h - cut); ctx.lineTo(x + w - cut, y + h);
    ctx.lineTo(x + r, y + h); ctx.quadraticCurveTo(x, y + h, x, y + h - r);
    ctx.lineTo(x, y + r); ctx.quadraticCurveTo(x, y, x + r, y); ctx.closePath();
  }
  function text(s, x, y, size, color, weight, align) {
    ctx.font = (weight || 400) + ' ' + size + 'px ' + MONO; ctx.fillStyle = color; ctx.textAlign = align || 'left'; ctx.textBaseline = 'middle';
    ctx.fillText(s, x, y);
  }
  function fit(s, max, size, weight) {
    ctx.font = (weight || 400) + ' ' + size + 'px ' + MONO;
    if (ctx.measureText(s).width <= max) return s;
    while (s.length > 1 && ctx.measureText(s + '…').width > max) s = s.slice(0, -1);
    return s + '…';
  }
  function short(ms) {
    var s = Math.max(0, Math.round(ms / 1000));
    if (s >= 3600) return Math.floor(s / 3600) + 'h' + ('0' + Math.floor((s % 3600) / 60)).slice(-2) + 'm';
    if (s >= 60) return Math.floor(s / 60) + 'm';
    return s + 's';
  }

  // Where a point sits on the path you -> relay -> machine, for u in 0..1.
  function along(v, u) {
    var a = { x: L.you.x + L.you.w / 2 - 4, y: L.you.y }, b = { x: L.relay.x, y: L.relay.y }, c = anchor(v);
    if (u < .5) { var k = u * 2; return { x: a.x + (b.x - a.x) * k, y: a.y + (b.y - a.y) * k }; }
    var t = (u - .5) * 2, m = mid(b, c);
    return { x: (1 - t) * (1 - t) * b.x + 2 * (1 - t) * t * m.x + t * t * c.x, y: (1 - t) * (1 - t) * b.y + 2 * (1 - t) * t * m.y + t * t * c.y };
  }
  function anchor(v) { return L.wide ? { x: v.x, y: v.y + L.th / 2 } : { x: v.x + L.tw / 2, y: v.y }; }
  function mid(b, c) { return L.wide ? { x: (b.x + c.x) / 2 + 10, y: b.y + (c.y - b.y) * .15 } : { x: b.x + (c.x - b.x) * .2, y: (b.y + c.y) / 2 + 6 }; }

  sim.on(function (type, m) {
    var v;
    if (type === 'launch') {
      var i = slots.indexOf(null); if (i < 0) i = 0;
      slots[i] = m.name; var p = L.slot(i);
      vis[m.name] = { m: m, slot: i, x: p.x, y: p.y, born: now(), ready: 0 };
      keys.push({ name: m.name, t0: now(), dur: 900 });
    } else if (type === 'ready') {
      v = vis[m.name]; if (v) { v.ready = now(); burst(v.x + L.tw / 2, v.y + L.th / 2, 26, C.signal); }
    } else if (type === 'exec') {
      v = vis[m.name]; if (!v) return;
      for (var k = 0; k < 7; k++) { packets.push({ name: m.name, t0: now() + k * 70, dur: 760, dir: 1 }); packets.push({ name: m.name, t0: now() + 520 + k * 70, dur: 760, dir: -1 }); }
      relayPulse = now();
    } else if (type === 'snooze') {
      v = vis[m.name]; if (v) { floats.push({ x: v.x + L.tw / 2, y: v.y, s: 'snoozed', c: C.lease, t0: now() }); v.snoozed = now(); }
    } else if (type === 'expire' || type === 'down') {
      v = vis[m.name]; if (!v) return;
      slots[v.slot] = null; delete vis[m.name];
      ghosts.push({ x: v.x, y: v.y, name: m.name, t0: now() });
      burst(v.x + L.tw / 2, v.y + L.th / 2, 70, type === 'expire' ? C.lease : C.ink2, true);
      floats.push({ x: v.x + L.tw / 2, y: v.y + L.th / 2, s: type === 'expire' ? 'deleted itself' : 'terminated', c: type === 'expire' ? C.lease : C.ink2, t0: now(), big: true });
    } else if (type === 'breach') { relayPulse = now(); burst(L.relay.x, L.relay.y, 50, sim.breached ? C.no : C.ok); }
    else if (type === 'wire') { relayPulse = now(); }
  });

  function burst(x, y, n, color, wideSpread) {
    if (reduce) return;
    for (var i = 0; i < n; i++) sparks.push({ x: x + (wideSpread ? rnd(-L.tw / 2, L.tw / 2) : 0), y: y + (wideSpread ? rnd(-L.th / 2, L.th / 2) : 0), vx: rnd(-1.6, 1.6), vy: rnd(-2.2, .6), life: 0, max: rnd(26, 70), s: rnd(1, 2.8), c: color });
  }

  function drawGrid(t) {
    ctx.fillStyle = C.rule; var step = 26, off = (t / 90) % step;
    for (var x = -step + off; x < W; x += step) for (var y = 6; y < H; y += step) { ctx.globalAlpha = .35; ctx.fillRect(x, y, 1, 1); }
    ctx.globalAlpha = 1;
  }

  function drawCloud() {
    var c = L.cloud;
    ctx.save(); ctx.setLineDash([5, 5]); ctx.strokeStyle = C.rule2; ctx.lineWidth = 1;
    rr(c.x, c.y, c.w, c.h, 10); ctx.stroke(); ctx.restore();
    ctx.fillStyle = 'rgba(123,162,255,.035)'; rr(c.x, c.y, c.w, c.h, 10); ctx.fill();
    text('YOUR CLOUD ACCOUNT', c.x + 12, c.y + 15, 10, C.ink3, 600);
    if (hot && hot.type === 'cloud' && Object.keys(vis).length < 8) text('+ runlease up', c.x + c.w - 12, c.y + 15, 9.5, C.lease, 600, 'right');
    hits.push({ type: 'cloud', x: c.x, y: c.y, w: c.w, h: c.h });
  }

  function drawYou(t) {
    var y = L.you, x0 = y.x - y.w / 2, y0 = y.y - y.h / 2, closed = !sim.lid;
    ctx.globalAlpha = closed ? .45 : 1;
    ctx.fillStyle = C.deck2; ctx.strokeStyle = hot && hot.type === 'you' ? C.ink : C.rule2; ctx.lineWidth = 1.2;
    if (closed) { rr(x0, y.y + y.h / 2 - 10, y.w, 10, 3); ctx.fill(); ctx.stroke(); }
    else {
      rr(x0 + 8, y0, y.w - 16, y.h - 14, 5); ctx.fill(); ctx.stroke();
      rr(x0, y.y + y.h / 2 - 12, y.w, 9, 3); ctx.fill(); ctx.stroke();
      text('$ runlease', x0 + 16, y0 + 17, 10, C.lease, 600);
      if (Math.floor(t / 520) % 2) { ctx.fillStyle = C.lease; ctx.fillRect(x0 + 16, y0 + 26, 5, 8); }
    }
    ctx.globalAlpha = 1;
    text(closed ? 'laptop closed' : 'your machine', y.x, y.y + y.h / 2 + 13, 11, closed ? C.lease : C.ink2, 600, 'center');
    text('login key stays here', y.x, y.y + y.h / 2 + 28, 9.5, C.ink3, 400, 'center');
    hits.push({ type: 'you', x: x0, y: y0, w: y.w, h: y.h + 30 });
  }

  function drawWeb() {
    var w = L.web, x0 = w.x - w.w / 2, y0 = w.y - w.h / 2, left = page.expiresAt - now(), k = page.alive ? Math.max(0, Math.min(1, left / page.total)) : 0;
    var soon = left < 30e3;
    ctx.fillStyle = C.deck; ticket(x0, y0, w.w, w.h); ctx.fill();
    ctx.strokeStyle = soon ? C.lease : (hot && hot.type === 'web' ? C.ink : C.rule2); ctx.lineWidth = 1.2; ticket(x0, y0, w.w, w.h); ctx.stroke();
    text('this-website', x0 + 9, y0 + 15, 11, C.ink, 600);
    text(page.alive ? 'a tab · real time' : 'deleted', x0 + 9, y0 + 29, 9, C.ink3);
    ctx.fillStyle = C.rule; ctx.fillRect(x0 + 9, y0 + w.h - 9, w.w - 30, 3);
    ctx.fillStyle = C.lease; ctx.fillRect(x0 + 9, y0 + w.h - 9, (w.w - 30) * k, 3);
    hits.push({ type: 'web', x: x0, y: y0, w: w.w, h: w.h });
  }

  function drawRelay(t) {
    var r = L.relay, bad = sim.breached, col = bad ? C.no : C.signal, pulse = Math.max(0, 1 - (now() - relayPulse) / 700);
    // you -> relay
    ctx.save(); ctx.strokeStyle = sim.lid ? C.rule2 : C.rule; ctx.lineWidth = 1.2; if (!sim.lid) ctx.setLineDash([2, 7]);
    ctx.beginPath(); ctx.moveTo(L.you.x + L.you.w / 2 - 4, L.you.y); ctx.lineTo(r.x - r.r, r.y); ctx.stroke(); ctx.restore();
    if (L.wide && (r.x - r.r) - (L.you.x + L.you.w / 2) > 96) text(sim.lid ? 'you dial out' : 'offline', (L.you.x + L.you.w / 2 + r.x - r.r) / 2, r.y - 10, 8.5, C.ink3, 400, 'center');
    ctx.save();
    ctx.shadowColor = col; ctx.shadowBlur = 14 + pulse * 26;
    ctx.fillStyle = bad ? 'rgba(255,122,107,.12)' : C.deck2; ctx.strokeStyle = col; ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.arc(r.x, r.y, r.r, 0, Math.PI * 2); ctx.fill(); ctx.stroke();
    ctx.restore();
    ctx.save(); ctx.strokeStyle = col; ctx.globalAlpha = .5; ctx.setLineDash([3, 6]); ctx.lineDashOffset = -t / 60;
    ctx.beginPath(); ctx.arc(r.x, r.y, r.r + 7, 0, Math.PI * 2); ctx.stroke(); ctx.restore();
    text('relay', r.x, r.y - 4, 12.5, C.ink, 600, 'center');
    text('no keys', r.x, r.y + 11, 9, C.ink3, 400, 'center');
    text(bad ? 'compromised' : 'sees ciphertext only', r.x, r.y + r.r + 20, 10, bad ? C.no : C.ink3, 600, 'center');
    if (now() - seesAt > 110) { seesAt = now(); sees = ''; for (var i = 0; i < 4; i++) sees += Math.floor(Math.random() * 65536).toString(16).padStart(4, '0') + ' '; }
    if (pulse > 0 || bad) text(sees, r.x, r.y + r.r + 34, 8.5, bad ? C.no : C.rule2, 400, 'center');
    hits.push({ type: 'relay', x: r.x - r.r - 8, y: r.y - r.r - 8, w: r.r * 2 + 16, h: r.r * 2 + 16 });
  }

  // The VM dials out to the relay. Drawn before any tile, so no line crosses one.
  function drawLink(v, t) {
    var m = v.m, k = (m.expiresAt - now()) / m.total, pending = m.state === 'pending', a = anchor(v), b = L.relay, mm = mid(b, a);
    ctx.save();
    ctx.globalAlpha = pending ? .35 : .8; ctx.strokeStyle = k < .2 && !pending ? C.lease : C.signal; ctx.lineWidth = 1; ctx.setLineDash([4, 6]); ctx.lineDashOffset = t / 40;
    ctx.beginPath(); ctx.moveTo(b.x + (L.wide ? b.r : 0), b.y + (L.wide ? 0 : b.r)); ctx.quadraticCurveTo(mm.x, mm.y, a.x, a.y); ctx.stroke();
    ctx.restore();
  }

  function place(v) { if (v.relayout) { var q = L.slot(v.slot); v.x = q.x; v.y = q.y; v.relayout = false; } }

  function drawMachine(v, t) {
    var m = v.m, age = now() - v.born, appear = reduce ? 1 : Math.min(1, age / 500), left = (m.expiresAt - now()), k = Math.max(0, Math.min(1, left / m.total));
    var x = v.x, y = v.y, w = L.tw, h = L.th, pending = m.state === 'pending', soon = k < .2 && !pending, isHot = hot && hot.type === 'machine' && hot.name === m.name;
    ctx.save();
    ctx.globalAlpha = appear;
    var s = .86 + .14 * appear; ctx.translate(x + w / 2, y + h / 2); ctx.scale(s, s); ctx.translate(-(x + w / 2), -(y + h / 2));
    if (soon) { ctx.shadowColor = C.lease; ctx.shadowBlur = 10 + 8 * Math.sin(t / 140); }
    ctx.fillStyle = isHot ? C.deck2 : C.deck; ticket(x, y, w, h); ctx.fill();
    ctx.shadowBlur = 0;
    ctx.strokeStyle = pending ? C.ink3 : soon ? C.lease : C.signal; ctx.lineWidth = isHot ? 1.8 : 1.2;
    if (pending) ctx.setLineDash([3, 4]);
    ticket(x, y, w, h); ctx.stroke(); ctx.setLineDash([]);
    var small = h < 44;
    text(fit(m.name, w - 62, 11, 600), x + 9, y + (small ? 12 : 14), 11, C.ink, 600);
    text(pending ? 'booting' : short(left * R.SCALE), x + w - 9, y + (small ? 12 : 14), 10, pending ? C.ink3 : soon ? C.lease : C.ink2, 600, 'right');
    if (!small) text(fit(m.size.replace('compute.', '') + ' · spot · ' + m.provider, w - 20, 8.5), x + 9, y + 28, 8.5, C.ink3);
    ctx.fillStyle = C.rule; ctx.fillRect(x + 9, y + h - 9, w - 30, 3);
    ctx.fillStyle = soon ? C.lease : C.signal; ctx.fillRect(x + 9, y + h - 9, (w - 30) * k, 3);
    if (v.snoozed && now() - v.snoozed < 600) { ctx.globalAlpha = (1 - (now() - v.snoozed) / 600) * .6; ctx.fillStyle = C.lease; ticket(x, y, w, h); ctx.fill(); }
    ctx.restore();
    hits.push({ type: 'machine', name: m.name, x: x, y: y, w: w, h: h });
  }

  function drawPackets() {
    var t = now(), keep = [];
    packets.forEach(function (p) {
      var v = vis[p.name]; if (!v) return;
      var u = (t - p.t0) / p.dur; if (u < 0) { keep.push(p); return; } if (u > 1) return;
      if (!sim.lid && p.dir === 1 && u < .5) return; // nothing leaves a closed laptop
      var q = along(v, p.dir === 1 ? u : 1 - u), near = Math.abs((p.dir === 1 ? u : 1 - u) - .5) < .09;
      ctx.save();
      ctx.shadowColor = sim.breached && near ? C.no : C.signal; ctx.shadowBlur = 8;
      ctx.fillStyle = near ? (sim.breached ? C.no : C.ink3) : C.signal;
      if (near) { ctx.font = '600 9px ' + MONO; ctx.textAlign = 'center'; ctx.textBaseline = 'middle'; ctx.fillText('▒', q.x, q.y); }
      else ctx.fillRect(q.x - 2, q.y - 2, 4, 4);
      ctx.restore(); keep.push(p);
    });
    packets = keep;
  }

  // The VM's host key is issued on your machine and travels through your
  // cloud's own API, not through the relay.
  function drawKeys() {
    var t = now(), keep = [];
    keys.forEach(function (k) {
      var v = vis[k.name]; if (!v) return;
      var u = (t - k.t0) / k.dur; if (u > 1) return;
      var a = { x: L.you.x, y: L.you.y - L.you.h / 2 }, c = { x: v.x + L.tw / 2, y: v.y + L.th / 2 };
      var m = L.wide ? { x: (a.x + c.x) / 2, y: Math.min(a.y, c.y) - 70 } : { x: (a.x + c.x) / 2 - 40, y: (a.y + c.y) / 2 };
      var e = 1 - Math.pow(1 - u, 3);
      var x = (1 - e) * (1 - e) * a.x + 2 * (1 - e) * e * m.x + e * e * c.x, y = (1 - e) * (1 - e) * a.y + 2 * (1 - e) * e * m.y + e * e * c.y;
      ctx.save(); ctx.globalAlpha = 1 - u * .4; ctx.fillStyle = C.ok; ctx.shadowColor = C.ok; ctx.shadowBlur = 10;
      ctx.beginPath(); ctx.arc(x, y, 3.5, 0, Math.PI * 2); ctx.fill(); ctx.fillRect(x + 3, y - 1, 8, 2); ctx.fillRect(x + 8, y, 2, 4);
      ctx.restore();
      if (u < .5 && L.wide) text('host key, via your cloud API', x + 12, y - 12, 9.5, C.ok);
      keep.push(k);
    });
    keys = keep;
  }

  function drawGhosts() {
    var t = now();
    ghosts = ghosts.filter(function (g) {
      var u = (t - g.t0) / 700; if (u > 1) return false;
      ctx.save(); ctx.globalAlpha = (1 - u) * .7; ctx.strokeStyle = C.lease; ctx.setLineDash([2, 5]); ctx.lineWidth = 1;
      ticket(g.x, g.y - u * 6, L.tw, L.th); ctx.stroke(); ctx.restore(); return true;
    });
    sparks = sparks.filter(function (p) {
      p.life++; if (p.life > p.max) return false;
      p.vy -= .02; p.x += p.vx; p.y += p.vy;
      var k = 1 - p.life / p.max; ctx.globalAlpha = k; ctx.fillStyle = p.c; ctx.fillRect(p.x, p.y, p.s * k + .5, p.s * k + .5); return true;
    });
    ctx.globalAlpha = 1;
    floats = floats.filter(function (f) {
      var u = (t - f.t0) / (f.big ? 1700 : 1000); if (u > 1) return false;
      ctx.globalAlpha = u < .7 ? 1 : (1 - u) / .3;
      text(f.s, f.x, f.y - u * 26, f.big ? 11 : 10, f.c, 600, 'center'); ctx.globalAlpha = 1; return true;
    });
  }

  function drawEmpty() {
    if (Object.keys(vis).length || ghosts.length) return;
    var c = L.cloud;
    text('nothing running', c.x + c.w / 2, c.y + c.h / 2 - 8, 11, C.ink3, 600, 'center');
    text('nothing billed', c.x + c.w / 2, c.y + c.h / 2 + 8, 10, C.rule2, 400, 'center');
  }

  function frame() {
    if (!visible) return;
    var t = now();
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    hits = [];
    drawGrid(t); drawCloud(); drawRelay(t); drawYou(t); drawWeb();
    Object.keys(vis).forEach(function (n) { place(vis[n]); drawLink(vis[n], t); });
    Object.keys(vis).forEach(function (n) { drawMachine(vis[n], t); });
    drawEmpty(); drawPackets(); drawKeys(); drawGhosts();
    hot = pick(mouse.x, mouse.y);
    cv.style.cursor = hot ? 'pointer' : 'default';
    requestAnimationFrame(frame);
  }

  function pick(x, y) {
    for (var i = hits.length - 1; i >= 0; i--) { var h = hits[i]; if (x >= h.x && x <= h.x + h.w && y >= h.y && y <= h.y + h.h) return h; }
    return null;
  }
  function pos(e) { var r = cv.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; }
  cv.addEventListener('mousemove', function (e) { mouse = pos(e); });
  cv.addEventListener('mouseleave', function () { mouse = { x: -1, y: -1 }; });
  cv.addEventListener('click', function (e) {
    var p = pos(e), h = pick(p.x, p.y); if (!h) return;
    if (h.type === 'machine') term.submit('runlease snooze ' + h.name + ' 1h');
    else if (h.type === 'cloud') term.submit('runlease up');
    else if (h.type === 'relay') R.setBreach(!sim.breached);
    else if (h.type === 'you') R.lid.click();
    else if (h.type === 'web') term.submit('runlease snooze this-website 3m');
  });

  if ('ResizeObserver' in window) new ResizeObserver(resize).observe(cv); else window.addEventListener('resize', resize);
  resize();
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(resize);

  var started = false;
  function intro() {
    if (started) return; started = true;
    term.note('A simulation of the runlease CLI. Type, or tap a command below.');
    setTimeout(function () {
      if (sim.machines.length) return; // the visitor got there first
      term.submit('runlease up -n agent-box --ttl 2h -d');
      term.submit('runlease exec agent-box -- uname -a');
    }, 700);
  }
  if ('IntersectionObserver' in window) {
    new IntersectionObserver(function (es) {
      es.forEach(function (e) {
        var was = visible; visible = e.isIntersecting;
        if (visible && !was) requestAnimationFrame(frame);
        if (e.isIntersecting && e.intersectionRatio > .25) intro();
      });
    }, { threshold: [0, .25, .5] }).observe(cv);
  } else { visible = true; requestAnimationFrame(frame); intro(); }
})();
