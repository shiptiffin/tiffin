// Tiffin analytics tracker: cookieless, no storage, ~1 KB minified.
//
// The box counts the first page load of every visit from its own edge logs,
// so this script only reports what the edge cannot see:
//   - client-side navigations in single-page apps (pushState, popstate)
//   - custom events: tiffin.track("Signup", { plan: "pro" })
//   - outbound link clicks and file downloads
// Add data-initial to the script tag when the page itself is NOT served by
// the Tiffin box (then the script also reports the first load).
;(function (w, d) {
  var s = d.currentScript
  if (!s) return
  var endpoint = s.getAttribute('data-api') || new URL(s.src).origin + '/e'
  var last = location.pathname + location.search

  var ref = d.referrer || null
  function send(name, props) {
    if (navigator.globalPrivacyControl) return // the visitor asked not to be counted
    var body = JSON.stringify({ n: name, u: location.href, r: ref, p: props || undefined })
    if (navigator.sendBeacon && navigator.sendBeacon(endpoint, body)) return
    try {
      fetch(endpoint, { method: 'POST', body: body, keepalive: true, credentials: 'omit' })
    } catch (e) {}
  }

  function nav() {
    var cur = location.pathname + location.search
    if (cur === last) return
    last = cur
    ref = null // the external referrer belongs to the first page only
    send('pageview')
  }

  var push = history.pushState
  history.pushState = function () {
    push.apply(this, arguments)
    nav()
  }
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
    pageview: function () { send('pageview') }
  }
  for (var i = 0; i < q.length; i++) w.tiffin.track.apply(null, q[i])
  if (s.hasAttribute('data-initial')) send('pageview')
})(window, document)
