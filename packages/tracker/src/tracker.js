// SPDX-License-Identifier: Apache-2.0
// Tiffin analytics tracker: cookieless, no storage, ~1 KB minified.
//
// The box counts the first page load of every visit from its own edge logs,
// so this script only reports what the edge cannot see:
//   - client-side navigations in single-page apps (pushState, replaceState, popstate)
//   - custom events: tiffin.track("Signup", { plan: "pro" })
//   - outbound link clicks and file downloads
// Add data-initial to the script tag when the page itself is NOT served by
// the Tiffin box (then the script also reports the first load).
;(function (w, d) {
  var s = d.currentScript
  if (!s) return
  var endpoint = s.getAttribute('data-api') || new URL(s.src).origin + '/e'
  var last = location.pathname
  // Browsers driven by test and scraping tools (WebDriver, Selenium,
  // Phantom, Nightmare, Cypress) are not visitors.
  var bot = navigator.webdriver || w._phantom || w.callPhantom || w.__nightmare || w.Cypress ||
    d.__selenium_unwrapped || d.__webdriver_evaluate || d.__driver_evaluate

  var ref = d.referrer || null
  function send(name, props) {
    if (bot || navigator.globalPrivacyControl) return // a test browser, or the visitor asked not to be counted
    var body = JSON.stringify({ n: name, u: location.href, r: ref, p: props || undefined })
    if (navigator.sendBeacon && navigator.sendBeacon(endpoint, body)) return
    try {
      fetch(endpoint, { method: 'POST', body: body, keepalive: true, credentials: 'omit' })
    } catch (e) {}
  }

  function pageview() { send('pageview') }

  // A navigation is a new path: a change to the query string alone
  // (filters, search, pagination state) is not a page view.
  function nav() {
    var cur = location.pathname
    if (cur === last) return
    last = cur
    ref = null // the external referrer belongs to the first page only
    pageview()
  }

  // Routers navigate with pushState and also with replaceState (redirects);
  // nav() ignores calls that keep the same path.
  function wrap(name) {
    var orig = history[name]
    history[name] = function () {
      var r = orig.apply(this, arguments)
      nav()
      return r
    }
  }
  wrap('pushState')
  wrap('replaceState')
  w.addEventListener('popstate', nav)

  d.addEventListener('click', function (e) {
    var a = e.target && e.target.closest && e.target.closest('a[href]')
    if (!a) return
    var u
    try { u = new URL(a.href, location.href) } catch (err) { return }
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return
    var to = u.origin + u.pathname // never the query or fragment: they can hold tokens
    if (/\.(pdf|zip|dmg|exe|msi|pkg|csv|xlsx?|docx?|pptx?|txt|rtf|mp3|mp4|mov|gz|tgz|7z|rar|apk|iso)$/i.test(u.pathname)) {
      send('File Download', { url: to })
    } else if (u.host !== location.host) {
      send('Outbound Link: Click', { url: to })
    }
  }, true)

  var q = (w.tiffin && w.tiffin.q) || []
  w.tiffin = {
    track: function (name, props) { send(String(name), props) },
    pageview: pageview
  }
  for (var i = 0; i < q.length; i++) w.tiffin.track.apply(null, q[i])
  // A prerendered page counts when the visitor opens it, if ever.
  if (s.hasAttribute('data-initial')) {
    if (d.prerendering) d.addEventListener('prerenderingchange', pageview)
    else pageview()
  }
})(window, document)
