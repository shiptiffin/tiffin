package edge

import "strings"

// The protection layer's pages: self-contained (no external assets), small,
// readable without CSS, light and dark, and calm under reduced motion.

// pageCSP is the Content-Security-Policy for these pages. With a nonce the
// page may run its own inline script and style; without, styles only.
func pageCSP(nonce string) string {
	if nonce == "" {
		return "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	}
	return "default-src 'none'; style-src 'nonce-" + nonce + "'; script-src 'nonce-" + nonce + "'; img-src data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

// pageCSS is shared by every page: warm grays, one brass accent.
const pageCSS = `
:root{--bg:#f5f1ea;--ink:#2a2620;--muted:#6d6457;--line:#ddd3c4;--brass:#a8761e;--brass-soft:#ecdcbc;color-scheme:light dark}
@media (prefers-color-scheme:dark){:root{--bg:#1b1916;--ink:#ede6da;--muted:#a59a8a;--line:#3a352e;--brass:#d6a64e;--brass-soft:#3b3122}}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--ink)}
body{font:16px/1.55 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;min-height:100vh;display:grid;place-items:center;padding:24px 16px}
main{width:100%;max-width:30rem}
h1{font-family:ui-rounded,"SF Pro Rounded","Nunito","Varela Round",system-ui,sans-serif;font-weight:650;font-size:1.65rem;line-height:1.2;letter-spacing:-.01em;margin:20px 0 10px}
p{margin:0 0 12px;color:var(--muted)}
p.lead{color:var(--ink)}
.mark{display:block;width:44px;height:52px;color:var(--brass)}
.fine{font-size:.85rem;margin-top:22px;padding-top:14px;border-top:1px solid var(--line)}
a{color:var(--brass)}
`

// tinMark is the stacked-tins line mark (decorative).
const tinMark = `<svg class="mark" viewBox="0 0 44 52" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` +
	`<path d="M15 9V5.5a2.5 2.5 0 0 1 2.5-2.5h9A2.5 2.5 0 0 1 29 5.5V9"/>` +
	`<g class="t t1"><rect x="7" y="9" width="30" height="11" rx="3"/></g>` +
	`<g class="t t2"><rect x="7" y="22" width="30" height="11" rx="3"/></g>` +
	`<g class="t t3"><rect x="7" y="35" width="30" height="13" rx="3"/></g>` +
	`<path d="M4 14v28M40 14v28"/></svg>`

func simplePage(title, heading, lead, fine string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta name="robots" content="noindex"><title>` + title + `</title><style>` + pageCSS + `</style></head><body><main>`)
	b.WriteString(tinMark)
	b.WriteString(`<h1>` + heading + `</h1><p class="lead">` + lead + `</p>`)
	if fine != "" {
		b.WriteString(`<p class="fine">` + fine + `</p>`)
	}
	b.WriteString(`</main></body></html>`)
	return b.String()
}

var (
	tooManyPage = simplePage("Too many requests", "Easy there, one moment",
		"You've sent a lot of requests in a short time, so this site is pausing yours for a few seconds.",
		"Wait a moment, then reload. If you were signing in, give it a minute before trying again.")
	blockedPage = simplePage("Blocked for now", "This connection is blocked for now",
		"Too many suspicious requests came from your network, so this site has stopped answering it for a while.",
		"Blocks lift on their own. If you think this is a mistake, tell the site's owner; they can lift it sooner.")
	wafPage = simplePage("Request blocked", "This request was blocked",
		"Something in it looked like an attack, so the site's firewall stopped it before it reached the app.",
		"If you were doing something ordinary, tell the site's owner what you were trying to do.")
)

// challengeHTML is the proof-of-work page. Placeholders: {{NONCE}} (CSP
// nonce), {{TOKEN}} (hex and dots only), {{BITS}} (an integer), {{NEXT}}
// (HTML-escaped), {{NOTE}} (fixed text).
const challengeHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>Just a moment</title><style nonce="{{NONCE}}">` + pageCSS + `
.meter{height:6px;border-radius:3px;background:var(--brass-soft);overflow:hidden;margin:18px 0 10px}
.meter i{display:block;height:100%;width:4%;background:var(--brass);transition:width .25s ease-out}
#s{color:var(--ink);font-weight:500}
button{font:inherit;color:var(--ink);background:none;border:1.5px solid var(--line);border-radius:6px;padding:6px 14px;cursor:pointer;margin-top:4px}
button:hover{border-color:var(--brass)}
.t{transform-box:fill-box;animation:settle 1.8s ease-in-out infinite}
.t2{animation-delay:.15s}.t3{animation-delay:.3s}
@keyframes settle{0%,60%,100%{transform:none}30%{transform:translateY(-2px)}}
.done .t{animation:none}
@media (prefers-reduced-motion:reduce){.t{animation:none}.meter i{transition:none}}
</style></head><body><main>` + tinMark + `
<h1>Just a quick check</h1>
<p class="lead">This site is busier than usual, so it asks each browser to do a small sum before letting it in. It takes a second or two, and you won't see it again today.</p>
{{NOTE}}<div class="meter" aria-hidden="true"><i id="bar"></i></div>
<p id="s" role="status">Working it out&hellip;</p>
<noscript><p>This check needs JavaScript. Turn it on and reload, or come back a little later.</p></noscript>
<form id="f" method="post" action="` + ChallengePath + `" data-bits="{{BITS}}">
<input type="hidden" name="token" value="{{TOKEN}}"><input type="hidden" name="nonce" value="">
<input type="hidden" name="next" value="{{NEXT}}">
<button type="submit" id="go" hidden>Continue</button></form>
</main><script nonce="{{NONCE}}">
(function(){
"use strict";
var f=document.getElementById("f"),s=document.getElementById("s"),bar=document.getElementById("bar");
var bits=parseInt(f.getAttribute("data-bits"),10),token=f.elements.token.value;
var K=new Int32Array([0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2]);
var W=new Int32Array(64);
function block(H,m,o){
  var i,a,b,c,d,e,g,h,k,t1,t2,x,y;
  for(i=0;i<16;i++){W[i]=(m[o+4*i]<<24)|(m[o+4*i+1]<<16)|(m[o+4*i+2]<<8)|m[o+4*i+3];}
  for(i=16;i<64;i++){x=W[i-15];y=W[i-2];
    W[i]=(((x>>>7)|(x<<25))^((x>>>18)|(x<<14))^(x>>>3))+W[i-7]+(((y>>>17)|(y<<15))^((y>>>19)|(y<<13))^(y>>>10))+W[i-16]|0;}
  a=H[0];b=H[1];c=H[2];d=H[3];e=H[4];g=H[5];h=H[6];k=H[7];
  for(i=0;i<64;i++){
    t1=k+(((e>>>6)|(e<<26))^((e>>>11)|(e<<21))^((e>>>25)|(e<<7)))+((e&g)^(~e&h))+K[i]+W[i]|0;
    t2=(((a>>>2)|(a<<30))^((a>>>13)|(a<<19))^((a>>>22)|(a<<10)))+((a&b)^(a&c)^(b&c))|0;
    k=h;h=g;g=e;e=d+t1|0;d=c;c=b;b=a;a=t1+t2|0;}
  H[0]=H[0]+a|0;H[1]=H[1]+b|0;H[2]=H[2]+c|0;H[3]=H[3]+d|0;H[4]=H[4]+e|0;H[5]=H[5]+g|0;H[6]=H[6]+h|0;H[7]=H[7]+k|0;
}
var IV=new Int32Array([0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19]);
var tb=[],i;for(i=0;i<token.length;i++){tb.push(token.charCodeAt(i)&255);}
var full=Math.floor(tb.length/64),mid=new Int32Array(IV),tail=tb.slice(full*64);
for(i=0;i<full;i++){block(mid,tb,i*64);}
var buf=new Uint8Array(192),H=new Int32Array(8);
function tryNonce(n){
  var ns=String(n),L=tail.length,j,len,total;
  for(j=0;j<L;j++){buf[j]=tail[j];}
  for(j=0;j<ns.length;j++){buf[L+j]=ns.charCodeAt(j);}
  len=L+ns.length;buf[len]=0x80;
  total=(len+9<=64)?64:(len+9<=128?128:192);
  for(j=len+1;j<total;j++){buf[j]=0;}
  var bitlen=(tb.length+ns.length)*8;
  buf[total-4]=(bitlen>>>24)&255;buf[total-3]=(bitlen>>>16)&255;buf[total-2]=(bitlen>>>8)&255;buf[total-1]=bitlen&255;
  H.set(mid);
  for(j=0;j<total;j+=64){block(H,buf,j);}
  return (H[0]>>>(32-bits))===0;
}
var n=0,expected=Math.pow(2,bits);
function step(){
  var until=Date.now()+40;
  while(Date.now()<until){
    for(var c=0;c<2000;c++,n++){
      if(tryNonce(n)){
        f.elements.nonce.value=String(n);bar.style.width="100%";
        document.body.className="done";s.textContent="Done. Taking you through…";
        f.submit();return;
      }
    }
  }
  bar.style.width=Math.min(96,4+92*(1-Math.exp(-n/expected)))+"%";
  setTimeout(step,0);
}
setTimeout(step,30);
})();
</script></body></html>`
