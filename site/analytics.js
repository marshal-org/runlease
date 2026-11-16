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
//   - "Do Not Track" or "Global Privacy Control" turns it off entirely.
//
// The key below is a public, write-only project key, meant for client code.
(function () {
  'use strict';
  var KEY = 'phc_uxg95JtK7nvyghhjy6DNPQNS7AMCnght3XdMLWLWvnXp';
  var ENDPOINT = 'https://us.i.posthog.com/i/v0/e/';

  // Either of the browser's "do not track me" signals turns this off.
  if (navigator.doNotTrack === '1' || window.doNotTrack === '1' || navigator.globalPrivacyControl === true) return;
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

  // The home page reports a handful of named interactions: that the page was
  // snoozed, that its lease ended, that a demo command was run. Only names
  // from this list are sent, with no free text, so nothing a visitor types
  // can leave the page this way.
  var NAMES = { site_lease_ended: 1, site_deleted_by_hand: 1, site_snoozed: 1, site_revived: 1, site_shared: 1, site_command: 1, site_relay_broken: 1, site_laptop_closed: 1 };
  var VERBS = { up: 1, exec: 1, push: 1, pull: 1, snooze: 1, down: 1, list: 1, doctor: 1, whoami: 1, help: 1, other: 1 };
  var sent = 0;
  window.addEventListener('runlease:track', function (e) {
    var d = e && e.detail; if (!d || !NAMES[d.event] || sent >= 40) return;
    sent += 1;
    var was = props.verb; delete props.verb;
    if (d.event === 'site_command') props.verb = VERBS[d.verb] ? d.verb : 'other';
    send(d.event);
    delete props.verb; if (was !== undefined) props.verb = was;
  });
  // That a link to Marshal or to the GitHub repository was followed, and
  // from which part of the page. Both values come from fixed lists.
  var OUT = { marshal: 'site_outbound_marshal', github: 'site_outbound_github' };
  var PLACES = { hero: 1, strip: 1, footer: 1, nav: 1, guide: 1 };
  document.addEventListener('click', function (e) {
    var a = e.target && e.target.closest ? e.target.closest('a[data-out]') : null;
    if (!a || !OUT[a.getAttribute('data-out')] || sent >= 40) return;
    sent += 1;
    var place = a.getAttribute('data-place');
    props.place = PLACES[place] ? place : 'other';
    send(OUT[a.getAttribute('data-out')]);
    delete props.place;
  });

  var left = false;
  window.addEventListener('pagehide', function () {
    if (left) return;
    left = true;
    send('$pageleave');
  });
})();
