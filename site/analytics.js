// Page views for runlease.online.
//
// This is the whole of it. No third-party code runs on this site: this file
// sends one small request to PostHog when a page is opened and one when it is
// left, and that is all.
//
//   - No cookies, and nothing stored in the browser.
//   - No visitor ID is made here. PostHog derives an anonymous one from a
//     salt it changes every day, so a visitor cannot be followed across days.
//   - PostHog is set to discard the IP address.
//   - What is sent: the page address without its query string, the page you
//     came from, your screen size, time zone and browser type.
//   - "Do Not Track" turns it off entirely.
//
// The key below is a public, write-only project key, meant for client code.
(function () {
  'use strict';
  var KEY = 'phc_uxg95JtK7nvyghhjy6DNPQNS7AMCnght3XdMLWLWvnXp';
  var ENDPOINT = 'https://us.i.posthog.com/i/v0/e/';

  if (navigator.doNotTrack === '1' || window.doNotTrack === '1') return;
  if (location.protocol !== 'https:') return; // local previews are not visits

  var ua = navigator.userAgent || '';

  function pick(rules, fallback) {
    for (var i = 0; i < rules.length; i++) if (rules[i][0].test(ua)) return rules[i][1];
    return fallback;
  }
  // Order matters: Edge and Opera also say "Chrome", Chrome also says "Safari".
  var browser = pick([[/Edg\//, 'Microsoft Edge'], [/OPR\//, 'Opera'], [/Firefox\//, 'Firefox'],
    [/Chrome\//, 'Chrome'], [/Safari\//, 'Safari']], 'Other');
  var os = pick([[/Android/, 'Android'], [/iPhone|iPad|iPod/, 'iOS'], [/Windows/, 'Windows'],
    [/Mac OS X/, 'Mac OS X'], [/Linux/, 'Linux']], 'Other');
  var device = /Mobi|iPhone|Android.*Mobile/.test(ua) ? 'Mobile' : (/iPad|Tablet|Android/.test(ua) ? 'Tablet' : 'Desktop');

  var referrer = document.referrer || '$direct';
  var referringDomain = '$direct';
  try { if (document.referrer) referringDomain = new URL(document.referrer).hostname; } catch (e) { /* keep $direct */ }

  var props = {
    // These two make PostHog treat the event as cookieless: it replaces the
    // placeholder ID with its own daily hash and assigns the session.
    $cookieless_mode: true,
    $raw_user_agent: ua,
    $current_url: location.origin + location.pathname,
    $host: location.host,
    $pathname: location.pathname,
    $referrer: referrer,
    $referring_domain: referringDomain,
    $browser: browser,
    $os: os,
    $device_type: device,
    $screen_width: screen.width,
    $screen_height: screen.height,
    $viewport_width: window.innerWidth,
    $viewport_height: window.innerHeight,
    $lib: 'runlease-site'
  };
  try { props.$timezone = Intl.DateTimeFormat().resolvedOptions().timeZone; } catch (e) { /* optional */ }

  // Campaign tags, if the link that brought the visitor here carried any.
  try {
    var q = new URLSearchParams(location.search);
    ['utm_source', 'utm_medium', 'utm_campaign', 'utm_content', 'utm_term'].forEach(function (k) {
      if (q.get(k)) props[k] = q.get(k).slice(0, 100);
    });
  } catch (e) { /* optional */ }

  function send(event) {
    var body = JSON.stringify({ api_key: KEY, event: event, distinct_id: '$posthog_cookieless', properties: props });
    try {
      fetch(ENDPOINT, { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: body })
        .catch(function () { /* analytics must never break the page */ });
    } catch (e) { /* same */ }
  }

  send('$pageview');
  var left = false;
  window.addEventListener('pagehide', function () {
    if (left) return;
    left = true;
    send('$pageleave');
  });
})();
